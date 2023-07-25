which go1.21rc3 > /dev/null || exit 1

pushd examples/bidirectional/; GOOS=wasip1 GOARCH=wasm go1.21rc3 build -o counter-go-grpc.wasm ./plugin-go-grpc/; COUNTER_PLUGIN=./counter-go-grpc.wasm go1.21rc3 run main.go put hello 3; cat log*; rm log* tmpplugin* counter-go-grpc.wasm; popd

