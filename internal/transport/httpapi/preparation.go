package httpapi

import "errors"

// errResponseWritten stops deferred input loading after the transport has
// answered a malformed request. The service must not perform the operation.
var errResponseWritten = errors.New("request response already written")
