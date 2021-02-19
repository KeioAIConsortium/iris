all: iris

iris: *.go
	go build -o iris
