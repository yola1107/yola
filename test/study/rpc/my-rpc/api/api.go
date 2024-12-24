package api

type HelloRequest struct {
	A int
	B int
}

type HelloResponse struct {
	Add int // +
	Sub int // -
	Mul int // *
}
