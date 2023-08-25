FROM golang:latest

WORKDIR /app

COPY app/go.mod ./
COPY app/go.sum ./

RUN go mod download

# TODO run with distroless or optimized for cloud run

COPY app/*.go ./

RUN go build -o ./bin

CMD [ "/app/bin" ]