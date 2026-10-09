package commitagent

import (
	"fmt"
	"io"
)

func newProgressLogger(output io.Writer) *progressLogger {
	return newCallbackProgressLogger(func(event ProgressEvent) {
		if output != nil {
			_, _ = fmt.Fprintf(output, "q commit · %s · %s\n", event.Stage, event.Message)
		}
	})
}
