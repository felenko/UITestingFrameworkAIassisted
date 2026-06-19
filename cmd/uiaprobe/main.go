// Command uiaprobe is a throwaway dev tool to validate UIA element resolution
// against a live window.
//
//	uiaprobe "<window title>" <automationId> [name] [controlType]   resolve a query
//	uiaprobe at <x> <y>                                             hit-test a screen point
package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/felenko/uitest/internal/core/platform"
)

func main() {
	if len(os.Args) >= 4 && os.Args[1] == "at" {
		probeAt(os.Args[2], os.Args[3])
		return
	}
	if len(os.Args) < 3 {
		fmt.Println(`usage:
  uiaprobe "<window title>" <automationId> [name] [controlType]
  uiaprobe at <x> <y>`)
		os.Exit(2)
	}
	title := os.Args[1]
	q := platform.UIAQuery{AutomationID: os.Args[2]}
	if len(os.Args) > 3 {
		q.Name = os.Args[3]
	}
	if len(os.Args) > 4 {
		q.ControlType = os.Args[4]
	}

	drv := platform.New()
	_ = drv.SetDPIAware()
	w, err := drv.FindWindow(platform.WindowQuery{Title: title, Strategy: "title"})
	if err != nil {
		fmt.Println("FindWindow:", err)
		os.Exit(1)
	}
	b, err := w.Bounds()
	if err == nil {
		fmt.Printf("window %q at (%d,%d) %dx%d\n", w.Title(), b.X, b.Y, b.Width, b.Height)
	}
	eb, err := drv.FindElement(w, q)
	if err != nil {
		fmt.Println("FindElement:", err)
		os.Exit(1)
	}
	fmt.Printf("element rect (%d,%d) %dx%d  center=(%d,%d)\n",
		eb.X, eb.Y, eb.Width, eb.Height, eb.X+eb.Width/2, eb.Y+eb.Height/2)
}

// probeAt hit-tests the UIA tree at a screen point and prints the harvested
// identity — the same code path the runner's find: harvest uses.
func probeAt(xs, ys string) {
	x, errX := strconv.Atoi(xs)
	y, errY := strconv.Atoi(ys)
	if errX != nil || errY != nil {
		fmt.Println("usage: uiaprobe at <x> <y>  (integer screen coordinates)")
		os.Exit(2)
	}
	drv := platform.New()
	_ = drv.SetDPIAware()
	n, err := drv.ElementAtPoint(platform.Point{X: x, Y: y})
	if err != nil {
		fmt.Println("ElementAtPoint:", err)
		os.Exit(1)
	}
	fmt.Printf("element at (%d,%d):\n", x, y)
	fmt.Printf("  automationId: %q\n", n.AutomationID)
	fmt.Printf("  name:         %q\n", n.Name)
	fmt.Printf("  controlType:  %q\n", n.ControlType)
	fmt.Printf("  rect:         (%d,%d) %dx%d  center=(%d,%d)\n",
		n.Bounds.X, n.Bounds.Y, n.Bounds.Width, n.Bounds.Height,
		n.Bounds.X+n.Bounds.Width/2, n.Bounds.Y+n.Bounds.Height/2)
}
