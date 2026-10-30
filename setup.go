package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/skip2/go-qrcode"
)

// setupClient keeps the interactive flow testable without a Telegram connection.
type setupClient interface {
	Status(context.Context) (*auth.Status, error)
	SendCode(context.Context, string, auth.SendCodeOptions) (tg.AuthSentCodeClass, error)
	ResendCode(context.Context, string, string) (tg.AuthSentCodeClass, error)
	SignIn(context.Context, string, string, string) (*tg.AuthAuthorization, error)
	Password(context.Context, string) (*tg.AuthAuthorization, error)
}

type setupPrompt func(context.Context, string, bool) (string, error)

func setupLogin(ctx context.Context, client setupClient, phone string, read setupPrompt, out io.Writer, now func() time.Time) error {
	status, err := client.Status(ctx)
	if err != nil {
		return loginError("check authorization", err)
	}

	if status.Authorized {
		return nil
	}

	f := &loginFlow{client: client, read: read, out: out, now: now, phone: phone}
	a := attempt{stage: "request login code"}
	a.response, a.err = client.SendCode(ctx, phone, auth.SendCodeOptions{})

	for {
		next, err := f.handle(ctx, a)
		if next == nil {
			return err
		}

		a = *next
	}
}

// attempt is one Telegram reply to a code request and the stage that produced it.
type attempt struct {
	response tg.AuthSentCodeClass
	err      error
	stage    string
}

// loginFlow is the state of one interactive code login.
type loginFlow struct {
	client setupClient
	read   setupPrompt
	out    io.Writer
	now    func() time.Time
	// The resend deadline belongs to a delivery response, so code retries keep it.
	eligible time.Time
	phone    string
	retry    bool
}

// handle answers one reply; a nil attempt ends the login with the returned error.
func (f *loginFlow) handle(ctx context.Context, a attempt) (*attempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("setup interrupted: %w", err)
	}

	if passwordNeeded(a.err) {
		return nil, setupPassword(ctx, f.client, f.read, f.out)
	}

	if a.err != nil {
		return nil, loginError(a.stage, a.err)
	}

	switch sent := a.response.(type) {
	case *tg.AuthSentCodeSuccess:
		return nil, authorized(sent)
	case *tg.AuthSentCode:
		return f.code(ctx, sent)
	default:
		return nil, errors.New("unsupported Telegram authorization response; check your account in an official Telegram client")
	}
}

func authorized(sent *tg.AuthSentCodeSuccess) error {
	if sent != nil {
		if a, ok := sent.Authorization.(*tg.AuthAuthorization); ok && a != nil {
			return nil
		}
	}

	return errors.New("login did not authorize an existing account; account registration is not supported")
}

// code prompts for the sent code and signs in, or returns the next reply to handle.
func (f *loginFlow) code(ctx context.Context, sent *tg.AuthSentCode) (*attempt, error) {
	if sent == nil || sent.PhoneCodeHash == "" {
		return nil, errors.New("received incomplete login-code information from Telegram")
	}

	if err := f.announce(sent); err != nil {
		return nil, err
	}

	f.retry = false

	code, resend, err := setupCode(ctx, sent, f.eligible, f.read, f.out, f.now)
	if err != nil {
		return nil, err
	}

	if resend {
		response, resendErr := f.client.ResendCode(ctx, f.phone, sent.PhoneCodeHash)
		return &attempt{response: response, err: resendErr, stage: "resend login code"}, nil
	}

	_, err = f.client.SignIn(ctx, f.phone, code, sent.PhoneCodeHash)
	if passwordNeeded(err) {
		return nil, setupPassword(ctx, f.client, f.read, f.out)
	}

	if tgerr.Is(err, "PHONE_CODE_INVALID") {
		// A typo must not cost a new code: re-prompt with the same hash.
		if _, err = fmt.Fprintln(f.out, "Telegram rejected the code (PHONE_CODE_INVALID); enter it again."); err != nil {
			return nil, loginError("display retry instructions", err)
		}

		f.retry = true

		return &attempt{response: sent, stage: "sign in"}, nil
	}

	return nil, loginError("sign in", err)
}

// announce reports a new code's delivery method and starts its resend deadline; retries keep both.
func (f *loginFlow) announce(sent *tg.AuthSentCode) error {
	if f.retry {
		return nil
	}

	delivery, err := codeDelivery(sent.Type)
	if err != nil {
		return err
	}

	if _, err = fmt.Fprintf(f.out, "Telegram reports delivery via %s. Receipt is not confirmed.\n", delivery); err != nil {
		return loginError("display delivery method", err)
	}

	timeout, _ := sent.GetTimeout()
	f.eligible = f.now().Add(time.Duration(max(timeout, 0)) * time.Second)

	return nil
}

func passwordNeeded(err error) bool {
	return errors.Is(err, auth.ErrPasswordAuthNeeded) || tgerr.Is(err, "SESSION_PASSWORD_NEEDED")
}

const noResendPath = "Telegram offers no resend path for this attempt. Enter the code or press Ctrl+C to exit."

func setupCode(ctx context.Context, sent *tg.AuthSentCode, eligible time.Time, read setupPrompt, out io.Writer, now func() time.Time) (string, bool, error) {
	available := resendAvailable(sent)
	if err := resendHint(out, available, eligible, now); err != nil {
		return "", false, err
	}

	for {
		code, err := readCode(ctx, read)
		if err != nil {
			return "", false, err
		}

		var stage, message string

		switch wait := eligible.Sub(now()); {
		case code == "":
			stage, message = "display input instructions", "Enter a login code, resend, or press Ctrl+C to exit."
		case code != "resend":
			return code, false, nil
		case !available:
			stage, message = "display resend instructions", noResendPath
		case wait > 0:
			stage, message = "display resend delay", fmt.Sprintf("Wait %s before entering resend again.", (wait+time.Second-1).Truncate(time.Second))
		default:
			return "", true, nil
		}

		if _, err = fmt.Fprintln(out, message); err != nil {
			return "", false, loginError(stage, err)
		}
	}
}

func resendAvailable(sent *tg.AuthSentCode) bool {
	next, ok := sent.GetNextType()
	return ok && next != nil
}

func resendHint(out io.Writer, available bool, eligible time.Time, now func() time.Time) error {
	message := noResendPath

	if available {
		wait := max(eligible.Sub(now())+time.Second-1, 0) / time.Second
		message = fmt.Sprintf("No code? Enter resend after %d seconds to request Telegram's next delivery method.", wait)
	}

	if _, err := fmt.Fprintln(out, message); err != nil {
		return loginError("display resend instructions", err)
	}

	return nil
}

func readCode(ctx context.Context, read setupPrompt) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("setup interrupted: %w", err)
	}

	code, err := read(ctx, "Telegram login code (or resend): ", true)
	if err != nil {
		return "", loginError("read login code", err)
	}

	if err = ctx.Err(); err != nil {
		return "", fmt.Errorf("setup interrupted: %w", err)
	}

	return code, nil
}

// qrClient keeps the QR login flow testable without a Telegram connection.
type qrClient interface {
	Auth(context.Context, qrlogin.LoggedIn, func(context.Context, qrlogin.Token) error, ...int64) (*tg.AuthAuthorization, error)
}

// qrPasswordClient covers the 2FA step that follows a confirmed QR scan.
type qrPasswordClient interface {
	Password(context.Context, string) (*tg.AuthAuthorization, error)
}

// setupQR authorizes without a login code: the Telegram phone app scans the token.
// Use when code delivery never arrives — Telegram silently drops codes after repeated requests.
// A confirmed scan on a 2FA-protected account still requires the password: Telegram reports
// SESSION_PASSWORD_NEEDED instead of completing authorization, so prompt for it like the code flow.
func setupQR(ctx context.Context, qr qrClient, passwords qrPasswordClient, loggedIn qrlogin.LoggedIn, out io.Writer, read setupPrompt) error {
	if _, err := fmt.Fprintln(out, "Scan the QR code with the Telegram app on the logged-in phone (Settings → Devices → Scan QR Code)."); err != nil {
		return loginError("display QR instructions", err)
	}

	_, err := qr.Auth(ctx, loggedIn, func(ctx context.Context, token qrlogin.Token) error {
		code, renderErr := qrcode.New(token.URL(), qrcode.Medium)
		if renderErr != nil {
			return loginError("render QR code", renderErr)
		}

		if _, err := fmt.Fprintln(out, code.ToString(false)); err != nil {
			return loginError("display QR code", err)
		}

		return ctx.Err()
	})
	if err == nil {
		return nil
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("setup interrupted: %w", err)
	}

	if passwordNeeded(err) {
		return setupPassword(ctx, passwords, read, out)
	}
	// QR failures carry no secrets (no phone or code involved): surface Telegram's
	// exact rejection instead of the generic message so the next step is actionable.
	if _, ok := tgerr.AsFloodWait(err); ok {
		return loginError("complete QR authorization", err)
	}

	if rpc, ok := tgerr.As(err); ok {
		return &setupError{message: "complete QR authorization: Telegram rejected the request: " + rpc.Type, cause: err}
	}

	return loginError("complete QR authorization", err)
}

func setupPassword(ctx context.Context, client qrPasswordClient, read setupPrompt, out io.Writer) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("setup interrupted: %w", err)
		}

		password, err := read(ctx, "Telegram 2FA password: ", false)
		if err != nil {
			return loginError("read 2FA password", err)
		}

		if err = ctx.Err(); err != nil {
			return fmt.Errorf("setup interrupted: %w", err)
		}

		_, err = client.Password(ctx, password)
		if !errors.Is(err, auth.ErrPasswordInvalid) {
			return loginError("verify 2FA password", err)
		}

		if _, err = fmt.Fprintln(out, "Incorrect 2FA password; try again."); err != nil {
			return loginError("display retry instructions", err)
		}
	}
}

func codeDelivery(kind tg.AuthSentCodeTypeClass) (string, error) {
	switch kind.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "an in-app service notification (check the Telegram service chat in your other logged-in sessions)", nil
	case *tg.AuthSentCodeTypeSMS:
		return "SMS (check your phone's messages)", nil
	case *tg.AuthSentCodeTypeSMSWord:
		return "SMS containing a word (enter the whole word)", nil
	case *tg.AuthSentCodeTypeSMSPhrase:
		return "SMS containing a phrase (enter the whole phrase)", nil
	case *tg.AuthSentCodeTypeCall:
		return "a voice call (enter the spoken code)", nil
	default:
		return "", fmt.Errorf("unsupported Telegram code delivery type %T; check your account in an official Telegram client", kind)
	}
}

// Keep error identity for callers without printing potentially sensitive SDK payloads.
type setupError struct {
	cause   error
	message string
}

func (e *setupError) Error() string { return e.message }
func (e *setupError) Unwrap() error { return e.cause }

func loginError(stage string, err error) error {
	if err == nil {
		return nil
	}

	message := "failed; check your connection and account in an official Telegram client"
	if wait, ok := tgerr.AsFloodWait(err); ok {
		message = fmt.Sprintf("Telegram rate limit; retry after %s", wait)
	} else if _, ok := errors.AsType[*auth.SignUpRequired](err); ok {
		message = "account registration is not supported"
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		message = "canceled or timed out"
	} else if errors.Is(err, auth.ErrPasswordInvalid) {
		message = "incorrect 2FA password"
	} else if errors.Is(err, errInteractiveTerminal) {
		message = errInteractiveTerminal.Error()
	} else if rpc, ok := tgerr.As(err); ok {
		switch rpc.Type {
		case "PHONE_NUMBER_INVALID", "PHONE_NUMBER_BANNED", "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD", "PHONE_CODE_INVALID", "PHONE_CODE_EXPIRED", "PHONE_CODE_EMPTY":
			message = "Telegram rejected the request: " + rpc.Type
		}
	}

	return &setupError{message: stage + ": " + message, cause: err}
}
