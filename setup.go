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
	if err := ctx.Err(); err != nil {
		return err
	}
	status, err := client.Status(ctx)
	if err != nil {
		return loginError("check authorization", err)
	}
	if status == nil {
		return errors.New("missing authorization status")
	}
	if status.Authorized {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	response, err := client.SendCode(ctx, phone, auth.SendCodeOptions{})
	stage := "request login code"
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, auth.ErrPasswordAuthNeeded) || tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
			return setupPassword(ctx, client, read)
		}
		if err != nil {
			return loginError(stage, err)
		}
		switch sent := response.(type) {
		case *tg.AuthSentCodeSuccess:
			if sent != nil {
				if a, ok := sent.Authorization.(*tg.AuthAuthorization); ok && a != nil {
					return nil
				}
			}
			return errors.New("login did not authorize an existing account; account registration is not supported")
		case *tg.AuthSentCode:
			if sent == nil || sent.PhoneCodeHash == "" {
				return errors.New("received incomplete login-code information from Telegram")
			}
			delivery, deliveryErr := codeDelivery(sent.Type)
			if deliveryErr != nil {
				return deliveryErr
			}
			if _, err := fmt.Fprintf(out, "Telegram reports delivery via %s. Receipt is not confirmed.\n", delivery); err != nil {
				return loginError("display delivery method", err)
			}
			code, resend, promptErr := setupCode(ctx, sent, read, out, now)
			if promptErr != nil {
				return promptErr
			}
			if resend {
				response, err = client.ResendCode(ctx, phone, sent.PhoneCodeHash)
				stage = "resend login code"
				continue
			}
			_, err = client.SignIn(ctx, phone, code, sent.PhoneCodeHash)
			if errors.Is(err, auth.ErrPasswordAuthNeeded) || tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				return setupPassword(ctx, client, read)
			}
			return loginError("sign in", err)
		default:
			return errors.New("unsupported Telegram authorization response; check your account in an official Telegram client")
		}
	}
}

func setupCode(ctx context.Context, sent *tg.AuthSentCode, read setupPrompt, out io.Writer, now func() time.Time) (string, bool, error) {
	timeout, _ := sent.GetTimeout()
	eligible := now().Add(time.Duration(max(timeout, 0)) * time.Second)
	next, available := sent.GetNextType()
	available = available && next != nil
	if available {
		if _, err := fmt.Fprintf(out, "No code? Enter resend after %d seconds to request Telegram's next delivery method.\n", max(timeout, 0)); err != nil {
			return "", false, loginError("display resend instructions", err)
		}
	} else {
		if _, err := fmt.Fprintln(out, "Telegram offers no resend path for this attempt. Enter the code or press Ctrl+C to exit."); err != nil {
			return "", false, loginError("display resend instructions", err)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		code, err := read(ctx, "Telegram login code (or resend): ", true)
		if err != nil {
			return "", false, loginError("read login code", err)
		}
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if code == "" {
			if _, err := fmt.Fprintln(out, "Enter a login code, resend, or press Ctrl+C to exit."); err != nil {
				return "", false, loginError("display input instructions", err)
			}
			continue
		}
		if code != "resend" {
			return code, false, nil
		}
		if !available {
			if _, err := fmt.Fprintln(out, "Telegram offers no resend path for this attempt. Enter the code or press Ctrl+C to exit."); err != nil {
				return "", false, loginError("display resend instructions", err)
			}
			continue
		}
		if wait := eligible.Sub(now()); wait > 0 {
			if _, err := fmt.Fprintf(out, "Wait %s before entering resend again.\n", (wait + time.Second - 1).Truncate(time.Second)); err != nil {
				return "", false, loginError("display resend delay", err)
			}
			continue
		}
		return "", true, nil
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
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
		return err
	}
	if errors.Is(err, auth.ErrPasswordAuthNeeded) || tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		return setupPassword(ctx, passwords, read)
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

func setupPassword(ctx context.Context, client qrPasswordClient, read setupPrompt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	password, err := read(ctx, "Telegram 2FA password: ", false)
	if err != nil {
		return loginError("read 2FA password", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = client.Password(ctx, password)
	return loginError("verify 2FA password", err)
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
	message string
	cause   error
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
