package middlewares

import "net/http"

// This funciton deals with HTTP requests sent to Discord
func TamperRequest(r *http.Request) (*http.Request, error) {
	return r, nil
}

// This function deals with HTTP responses received from Discord for a given
// request
func TamperResponse(req *http.Request, resp *http.Response) (*http.Response, error) {
	return resp, nil
}
