package log

import (
	"fmt"
	"os"
)

var Basic bool
var Debug bool

func Basicf(format string, args ...interface{}) {
	if Basic {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func Debugf(format string, args ...interface{}) {
	if Debug {
		fmt.Fprintf(os.Stderr, "[DEBUG] "+format+"\n", args...)
	}
}
