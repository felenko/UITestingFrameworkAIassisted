//go:build !windows

package uia

import "fmt"

// Query selects a control in the UIA tree.
type Query struct {
	AutomationID string
	Name         string
	ControlType  string
}

// Rect is a control's screen rectangle.
type Rect struct{ X, Y, Width, Height int }

// Center returns the rectangle's midpoint.
func (r Rect) Center() (int, int) { return r.X + r.Width/2, r.Y + r.Height/2 }

// Automation is a no-op on non-Windows platforms.
type Automation struct{}

// Element is a no-op on non-Windows platforms.
type Element struct{}

func unsupported() error { return fmt.Errorf("uia: UI Automation is only supported on Windows") }

// New always fails off Windows.
func New() (*Automation, error) { return nil, unsupported() }

func (a *Automation) Close()                                {}
func (a *Automation) Find(uintptr, Query) (*Element, error) { return nil, unsupported() }

func (e *Element) Release()                  {}
func (e *Element) BoundingRect() (Rect, error) { return Rect{}, unsupported() }
func (e *Element) IsEnabled() bool           { return false }
func (e *Element) AutomationID() string      { return "" }
func (e *Element) Name() string              { return "" }
func (e *Element) ControlType() int32        { return 0 }
