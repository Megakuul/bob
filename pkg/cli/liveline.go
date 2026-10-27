package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/megakuul/bob/pkg/bob/progress"
)

// liveLine is the live status line of slow steps (SPEC §02.5 rule 7): on a terminal, one
// self-updating line on stderr with a spinner, the phase the engine reports through
// progress.Phase and the time the phase has taken. It appears only when a step takes longer than
// liveDelay, is erased before any other output (stdout and stderr go through lineWriter) and
// never exists without a terminal, so output that is not a terminal is unchanged.
type liveLine struct {
	raw   io.Writer // the real stderr
	ascii bool      // NO_COLOR: no braille spinner
	width func() int
	now   func() time.Time
	delay time.Duration // a step must run this long before the line appears
	tick  time.Duration
	grace time.Duration // phases that follow each other within this belong to one step

	mu         sync.Mutex
	stack      []*livePhase
	activeFrom time.Time // the step began (or the last output happened)
	emptySince time.Time // the stack became empty (zero while it is not)
	drawn      bool
	frame      int
	stopped    bool

	quit chan struct{}
	done chan struct{}
}

type livePhase struct {
	text  string
	start time.Time
	weak  bool
}

const (
	liveDelay = 400 * time.Millisecond
	liveTick  = 100 * time.Millisecond
	liveGrace = 300 * time.Millisecond
)

var (
	brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	asciiFrames   = []string{"|", "/", "-", "\\"}
)

// newLiveLine starts the line for stderr raw.
func newLiveLine(raw io.Writer, ascii bool) *liveLine {
	l := &liveLine{raw: raw, ascii: ascii, now: time.Now, delay: liveDelay, tick: liveTick, grace: liveGrace,
		quit: make(chan struct{}), done: make(chan struct{})}
	l.width = func() int { return terminalWidth(raw) }
	go l.loop()
	return l
}

// liveWanted reports whether the command shows the line: a terminal on stderr that is not
// dumb, no --json/-q/-vv, no CI, and not a command whose output is read by a program.
func liveWanted(o *out, getenv func(string) string) bool {
	switch o.command {
	case "env", "hook", "exec", "completion", "allow", "deny", "version", "help":
		return false
	}
	if o.json || o.quiet || o.verbose >= 2 || !isTerminal(o.errw) {
		return false
	}
	if t := getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	if getenv("CI") != "" {
		return false
	}
	switch strings.ToLower(getenv("BOB_PROGRESS")) {
	case "0", "never", "off", "false", "no":
		return false
	}
	return true
}

// attachLive makes the line part of the command's output and context when it is wanted: the
// writers of o clear it before they write, and the returned context carries the reporter.
// The returned stop erases the line and ends it.
func (o *out) attachLive(ctx context.Context, getenv func(string) string) (context.Context, func()) {
	if !liveWanted(o, getenv) {
		return ctx, func() {}
	}
	l := newLiveLine(o.errw, getenv("NO_COLOR") != "")
	o.phase = l.Begin
	o.liveStop = l.stop
	o.w = &lineWriter{l: l, w: o.w}
	o.errw = &lineWriter{l: l, w: o.errw}
	return progress.With(ctx, l), l.stop
}

// Begin implements progress.Reporter.
func (l *liveLine) Begin(text string, weak bool) func() {
	now := l.now()
	p := &livePhase{text: text, start: now, weak: weak}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.stack) == 0 && (l.emptySince.IsZero() || now.Sub(l.emptySince) > l.grace) {
		l.activeFrom = now
	}
	l.stack = append(l.stack, p)
	l.emptySince = time.Time{}
	l.redraw() // a step that goes on in the next phase keeps its line without a blink
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for i, q := range l.stack {
			if q == p {
				l.stack = append(l.stack[:i], l.stack[i+1:]...)
				break
			}
		}
		if len(l.stack) == 0 {
			l.emptySince = l.now()
			l.erase() // whatever runs next (a program of bob run, a shell) finds a clean line
		}
	}
}

// loop redraws the line every tick until stop.
func (l *liveLine) loop() {
	defer close(l.done)
	t := time.NewTicker(l.tick)
	defer t.Stop()
	for {
		select {
		case <-l.quit:
			return
		case <-t.C:
			l.mu.Lock()
			l.redraw()
			l.mu.Unlock()
		}
	}
}

// redraw draws, updates or erases the line. l.mu is held.
func (l *liveLine) redraw() {
	if l.stopped {
		return
	}
	now := l.now()
	n := len(l.stack)
	if n == 0 {
		l.erase()
		return
	}
	top := l.stack[n-1]
	for i := n - 1; i >= 0 && top.weak; i-- { // the newest phase that is not a detail
		if !l.stack[i].weak {
			top = l.stack[i]
		}
	}
	text, start := top.text, top.start
	if text == "" || now.Sub(l.activeFrom) < l.delay {
		return
	}
	frames := brailleFrames
	if l.ascii {
		frames = asciiFrames
	}
	line := frames[l.frame%len(frames)] + " " + text
	l.frame++
	if d := now.Sub(start); d >= time.Second {
		line += " · " + elapsed(d)
	}
	if w := l.width(); w > 8 {
		if r := []rune(line); len(r) > w-1 {
			line = string(r[:w-2]) + "…"
		}
	}
	fmt.Fprint(l.raw, "\r\x1b[K"+line)
	l.drawn = true
}

// elapsed is "7s" or "2m05s".
func elapsed(d time.Duration) string {
	s := int(d / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// erase removes the line. l.mu is held.
func (l *liveLine) erase() {
	if l.drawn {
		fmt.Fprint(l.raw, "\r\x1b[K")
		l.drawn = false
	}
}

// stop erases the line and ends the loop; later phases and writes pass through.
func (l *liveLine) stop() {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return
	}
	l.erase()
	l.stopped = true
	l.mu.Unlock()
	close(l.quit)
	<-l.done
}

// lineWriter erases the line before every write of other output; the line comes back after
// liveDelay if a phase is still running.
type lineWriter struct {
	l *liveLine
	w io.Writer
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	l := lw.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.stopped {
		l.erase()
		l.activeFrom = l.now()
	}
	return lw.w.Write(p)
}

// Underlying is the writer behind lw (isTerminal looks through it).
func (lw *lineWriter) Underlying() io.Writer { return lw.w }

// terminalWidth is the number of columns of the terminal w, 0 if unknown.
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.Col)
}
