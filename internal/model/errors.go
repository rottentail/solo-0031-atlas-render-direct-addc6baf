package model

import "fmt"

// Error is the domain error envelope. Status, Code and Message are the legacy
// contract; Diagnostics is the opt-in structured detail. When Diagnostics is
// empty the legacy envelope is the only representation and nothing changes for
// existing clients.
type Error struct {
	Status      int
	Code        string
	Message     string
	Diagnostics []Diagnostic
	Subject     DiagnosticSubject
}

func (e *Error) Error() string { return e.Message }
func Invalid(format string, args ...any) error {
	return &Error{Status: 400, Code: "invalid", Message: fmt.Sprintf(format, args...)}
}
func Conflict(message string) error { return &Error{Status: 409, Code: "conflict", Message: message} }
func Missing(message string) error  { return &Error{Status: 404, Code: "missing", Message: message} }
func Gone(message string) error     { return &Error{Status: 410, Code: "gone", Message: message} }
func Busy(message string) error     { return &Error{Status: 429, Code: "capacity", Message: message} }

// Diagnosed returns a 400 rejection that keeps the legacy code "invalid" and
// the legacy message untouched, while carrying diagnostics that point back at
// the checked content.
func Diagnosed(message string, diagnostics ...Diagnostic) error {
	if len(diagnostics) == 0 {
		return Invalid("%s", message)
	}
	return &Error{Status: 400, Code: "invalid", Message: message, Diagnostics: diagnostics}
}

// DiagnosedStatus is Diagnosed for non-400 rejections, such as render capacity.
// legacyCode is what pre-diagnostic clients keep seeing.
func DiagnosedStatus(status int, legacyCode, message string, diagnostics ...Diagnostic) error {
	if len(diagnostics) == 0 {
		return &Error{Status: status, Code: legacyCode, Message: message}
	}
	return &Error{Status: status, Code: legacyCode, Message: message, Diagnostics: diagnostics}
}
