package narc

import (
	"bytes"
	"fmt"
	"io"
	. "launchpad.net/gocheck"
	"regexp"
	"sync"
	"time"
)

func expect(c *C, expector *Expector, output string) {
	err := expector.Expect(output)
	if err != nil {
		c.Error(err.Error())
	}
}

type Expector struct {
	output         io.Reader
	defaultTimeout time.Duration

	outputError chan error
	listen      chan bool

	offset int
	buffer *bytes.Buffer

	sync.RWMutex
}

type ExpectationFailed struct {
	Wanted string
	Got    string
}

func (e ExpectationFailed) Error() string {
	return fmt.Sprintf("Expected to see '%s', got: %#v", e.Wanted, e.Got)
}

func NewExpector(out io.Reader, defaultTimeout time.Duration) *Expector {
	e := &Expector{
		output:         out,
		defaultTimeout: defaultTimeout,

		outputError: make(chan error),
		listen:      make(chan bool),

		buffer: new(bytes.Buffer),
	}

	go e.monitor()

	return e
}

func (e *Expector) Expect(pattern string) error {
	return e.ExpectWithTimeout(pattern, e.defaultTimeout)
}

func (e *Expector) ExpectWithTimeout(pattern string, timeout time.Duration) error {
	regexp, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	cancel := make(chan bool, 1)

	select {
	case <-e.match(regexp, cancel):
		return nil
	case err := <-e.outputError:
		return err
	case <-time.After(timeout):
		cancel <- true

		return ExpectationFailed{
			Wanted: pattern,
			Got:    string(e.nextOutput()),
		}
	}
}

func (e *Expector) match(regexp *regexp.Regexp, cancel chan bool) chan bool {
	matchResult := make(chan bool)

	go func() {
		for {
			found := regexp.FindIndex(e.nextOutput())
			if found != nil {
				e.forwardOutput(found[1])
				matchResult <- true
				break
			}

			select {
			case <-e.listen:
			case <-time.After(100 * time.Millisecond):
			case <-cancel:
				return
			}
		}
	}()

	return matchResult
}

func (e *Expector) monitor() {
	var buf [1024]byte

	for {
		read, err := e.output.Read(buf[:])

		if read > 0 {
			e.addOutput(buf[:read])
		}

		if err != nil {
			e.outputError <- err
			break
		}

		e.notify()
	}
}

func (e *Expector) addOutput(out []byte) {
	e.Lock()
	defer e.Unlock()

	e.buffer.Write(out)

}

func (e *Expector) forwardOutput(count int) {
	e.Lock()
	defer e.Unlock()

	e.buffer.Next(count)

}
func (e *Expector) nextOutput() []byte {
	e.RLock()
	defer e.RUnlock()

	return e.buffer.Bytes()
}

func (e *Expector) notify() {
	select {
	case e.listen <- true:
	default:
	}
}
