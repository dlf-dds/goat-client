package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

func TestNotifier_NilSafe(t *testing.T) {
	// notifier.Send on nil receiver must not panic — paranoid guard
	// since the GUI builds notifier inside newMainWindow and shouldn't
	// hand out nil, but the production code's explicit nil check at
	// notifier.go:18 deserves a regression bar.
	var n *notifier
	n.Send("title", "body")
}

func TestNotifier_NilAppSafe(t *testing.T) {
	n := &notifier{app: nil}
	n.Send("title", "body")
}

func TestNotifier_SendsNotification(t *testing.T) {
	app := test.NewTempApp(t)
	n := newNotifier(app)

	// AssertNotificationSent intercepts the SendNotification call
	// during the closure execution. It asserts the captured payload
	// matches the expected fyne.Notification.
	want := fyne.NewNotification("hello", "world")
	test.AssertNotificationSent(t, want, func() { n.Send("hello", "world") })
}
