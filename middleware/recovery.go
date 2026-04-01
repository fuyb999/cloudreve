package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/gin-gonic/gin"
)

// Recovery recovers request panics and routes them through project logging.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		l := logging.FromContext(c)
		message := "HTTP panic recovered"
		if c != nil && c.Request != nil {
			message = "HTTP panic recovered: " + c.Request.Method + " " + c.Request.URL.String()
		}

		if isBrokenPipe(recovered) {
			l.Warning("%s err=%v", message, recovered)
			c.Abort()
			return
		}

		logging.Recover(l, "%s err=%v", message, recovered)
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}

func isBrokenPipe(recovered any) bool {
	var opErr *net.OpError
	if !errors.As(asError(recovered), &opErr) {
		return false
	}

	var syscallErr *os.SyscallError
	if !errors.As(opErr, &syscallErr) {
		return false
	}

	errText := strings.ToLower(syscallErr.Error())
	return strings.Contains(errText, "broken pipe") || strings.Contains(errText, "connection reset by peer")
}

func asError(recovered any) error {
	switch v := recovered.(type) {
	case nil:
		return nil
	case error:
		return v
	default:
		return fmt.Errorf("%v", v)
	}
}
