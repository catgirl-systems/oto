package ipc

// HTTPError distinguishes a daemon rejection from a lost/ambiguous transport
// response, so editors can correct rejected input without retrying an unknown send.
type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string { return e.Message }
