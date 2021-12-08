FROM rustlang/rust:nightly-alpine as build
RUN apk add --no-cache git musl-dev protoc
WORKDIR /tmp
RUN git clone https://github.com/tokio-rs/console.git
RUN cd console && cargo +nightly build --release --example app

FROM alpine:3.12 as runtime
COPY --from=build /tmp/console/target/release/examples/app ./bin/console-example
ENTRYPOINT ["./bin/console-example"]
