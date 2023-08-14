package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin/config"
	"github.com/hashicorp/go-plugin/internal/constants"
)

var _ Runner = (*ContainerRunner)(nil)

// ContainerRunner implements the Executor interface by running a container.
type ContainerRunner struct {
	logger hclog.Logger

	cmd    *exec.Cmd
	config *config.ContainerConfig

	hostSocketDir string

	dockerClient *client.Client
	stdout       io.ReadCloser
	stderr       io.ReadCloser

	image string
	id    string
}

// NewContainerRunner must be passed a cmd that hasn't yet been started.
func NewContainerRunner(logger hclog.Logger, cmd *exec.Cmd, cfg *config.ContainerConfig, hostSocketDir string) (*ContainerRunner, error) {
	client, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	// TODO: Support overriding entrypoint, args, and working dir from cmd
	const containerSocketDir = "/tmp"
	cfg.HostConfig.Mounts = append(cfg.HostConfig.Mounts, mount.Mount{
		Type:     mount.TypeBind,
		Source:   hostSocketDir,
		Target:   containerSocketDir,
		ReadOnly: false,
		BindOptions: &mount.BindOptions{
			Propagation:  mount.PropagationRShared,
			NonRecursive: true,
		},
		Consistency: mount.ConsistencyDefault,
		// VolumeOptions:  &mount.VolumeOptions{},
		// TmpfsOptions:   &mount.TmpfsOptions{},
		// ClusterOptions: &mount.ClusterOptions{},
	})
	// TODO(tomhjp): Copy and edit instead of edit in place.
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", constants.EnvUnixSocketDir, containerSocketDir))
	if cfg.UnixSocketGroup != 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", constants.EnvUnixSocketGroup, cfg.UnixSocketGroup))
	}
	cfg.ContainerConfig.Env = cmd.Env

	return &ContainerRunner{
		logger:        logger,
		cmd:           cmd,
		config:        cfg,
		hostSocketDir: hostSocketDir,
		dockerClient:  client,
		image:         cfg.ContainerConfig.Image,
	}, nil
}

func (c *ContainerRunner) Start() error {
	ctx := context.Background()
	resp, err := c.dockerClient.ContainerCreate(ctx, c.config.ContainerConfig, c.config.HostConfig, c.config.NetworkConfig, nil, "")
	if err != nil {
		return err
	}
	c.id = resp.ID

	if err := c.dockerClient.ContainerStart(ctx, c.id, types.ContainerStartOptions{}); err != nil {
		return err
	}

	// ContainerLogs combines stdout and stderr.
	logReader, err := c.dockerClient.ContainerLogs(ctx, c.id, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return err
	}

	// c.logger.Debug("tmp dir", "tmp", c.config.DockerConfig.TmpDir)
	// Split logReader stream into distinct stdout and stderr readers.
	var stdoutWriter, stderrWriter io.WriteCloser
	c.stdout, stdoutWriter = io.Pipe()
	c.stderr, stderrWriter = io.Pipe()
	go func() {
		defer func() {
			c.logger.Trace("container logging goroutine shutting down", "id", c.id)
			logReader.Close()
			stdoutWriter.Close()
			stderrWriter.Close()
		}()

		// StdCopy will run until it receives EOF from logReader
		if _, err := stdcopy.StdCopy(stdoutWriter, stderrWriter, logReader); err != nil {
			c.logger.Error("error streaming logs from container", "id", c.id, "error", err)
		}
	}()

	return nil
}

func (c *ContainerRunner) Wait() error {
	statusCh, errCh := c.dockerClient.ContainerWait(context.Background(), c.id, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case st := <-statusCh:
		c.logger.Info("received status update", "status", st)
		if st.Error != nil {
			return errors.New(st.Error.Message)
		}
		return nil
	}

	// unreachable
	return nil
}

func (c *ContainerRunner) Kill() error {
	defer c.dockerClient.Close()
	defer os.RemoveAll(c.hostSocketDir)
	if c.id != "" {
		return c.dockerClient.ContainerStop(context.Background(), c.id, container.StopOptions{})
	}

	return nil
}

func (c *ContainerRunner) Stdout() io.ReadCloser {
	return c.stdout
}

func (c *ContainerRunner) Stderr() io.ReadCloser {
	return c.stderr
}

func (c *ContainerRunner) ResolveAddr(network, address string) (net.Addr, error) {
	switch network {
	case "unix":
		if !strings.HasPrefix(address, "PLUGIN_UNIX_SOCKET_DIR:") {
			return nil, errors.New("plugin is running inside container but needs an update to be compatible")
		}

		address = path.Join(c.hostSocketDir, strings.TrimPrefix(address, "PLUGIN_UNIX_SOCKET_DIR:"))
		return net.ResolveUnixAddr("unix", address)
	default:
		return nil, fmt.Errorf("unsupported address: %s, %s", network, address)
	}
}

func (c *ContainerRunner) Name() string {
	return c.image
}

func (c *ContainerRunner) ID() string {
	return c.id
}
