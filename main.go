package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName              = "blindspot_session"
	adminSessionCookieName         = "blindspot_admin_session"
	adminEmail                     = "admin@admin.com"
	questionMaxLength              = 2000
	profileNameMaxLength           = 12
	answerMaxLength                = 4000
	passwordMinLength              = 8
	resetTokenTTLHours             = 2
	emailValidationTTLHours        = 48
	userStatusValidated            = "validated"
	userStatusUnvalidated          = "unvalidated"
	questionStatusActive           = "active"
	questionStatusDone             = "completed"
	usersTableName                 = "app_users"
	questionsTableName             = "app_questions"
	responsesTableName             = "app_responses"
	resetTokensTableName           = "app_password_reset_tokens"
	emailValidationTokensTableName = "app_email_validation_tokens"
)

var profileNameAlphabet = []byte("abcdefghijklmnopqrstuvwxyz0123456789")

var errQuestionLocked = errors.New("active question already has responses")

type App struct {
	pb          *pocketbase.PocketBase
	templateDir string
	config      AppConfig
}

type userRow struct {
	ID           string `db:"id"`
	Email        string `db:"email"`
	PasswordHash string `db:"password_hash"`
	ProfileName  string `db:"profile_name"`
	UserStatus   string `db:"user_status"`
	CreatedAt    string `db:"created_at"`
}

type questionRow struct {
	ID           string `db:"id"`
	UserID       string `db:"user_id"`
	QuestionText string `db:"question_text"`
	Status       string `db:"status"`
	CompletedAt  string `db:"completed_at"`
	CreatedAt    string `db:"created_at"`
}

type responseRow struct {
	ID             string `db:"id"`
	UserID         string `db:"user_id"`
	QuestionID     string `db:"question_id"`
	AnswerText     string `db:"answer_text"`
	DeliveryStatus string `db:"delivery_status"`
	DeliveryError  string `db:"delivery_error"`
	CreatedAt      string `db:"created_at"`
}

type passwordResetTokenRow struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	TokenHash string `db:"token_hash"`
	ExpiresAt string `db:"expires_at"`
	UsedAt    string `db:"used_at"`
	CreatedAt string `db:"created_at"`
}

type emailValidationTokenRow struct {
	ID        string `db:"id"`
	UserID    string `db:"user_id"`
	TokenHash string `db:"token_hash"`
	ExpiresAt string `db:"expires_at"`
	UsedAt    string `db:"used_at"`
	CreatedAt string `db:"created_at"`
}

type UserView struct {
	ID             string
	Email          string
	ProfileName    string
	UserStatus     string
	ActiveQuestion *QuestionView
}

type QuestionView struct {
	ID           string
	QuestionText string
	Status       string
	Created      string
	CompletedAt  string
}

type ResponseView struct {
	ID             string
	AnswerText     string
	DeliveryStatus string
	DeliveryError  string
	Created        string
}

type QuestionGroupView struct {
	QuestionView
	Responses []ResponseView
}

func main() {
	app := pocketbase.New()
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "serve")
	}

	webApp := &App{
		pb:          app,
		templateDir: "src/sample_python_project/templates",
		config:      loadAppConfig("config.json"),
	}

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		if err := webApp.ensureSchema(se.App); err != nil {
			return err
		}

		se.Router.GET("/static/{path...}", apis.Static(os.DirFS("src/sample_python_project/static"), false))
		se.Router.GET("/", webApp.handleHome)
		se.Router.GET("/admin-panel/login", webApp.handleAdminLoginPage)
		se.Router.POST("/admin-panel/login", webApp.handleAdminLogin)
		se.Router.POST("/admin-panel/logout", webApp.handleAdminLogout)
		se.Router.GET("/admin-panel", webApp.handleAdminDashboard)
		se.Router.GET("/admin-panel/mail-test", webApp.handleAdminMailTestPage)
		se.Router.POST("/admin-panel/mail-test", webApp.handleAdminMailTest)
		se.Router.GET("/admin-panel/users/{userID}", webApp.handleAdminUserDetail)
		se.Router.POST("/admin-panel/users/{userID}/delete", webApp.handleAdminDeleteUser)
		se.Router.GET("/about", webApp.handleAboutPage)
		se.Router.GET("/privacy", webApp.handlePrivacyPage)
		se.Router.GET("/privacy/legal", webApp.handleGeneratedPrivacyPage)
		se.Router.GET("/privacy/generated_privacy_files/{path...}", apis.Static(os.DirFS(filepath.Join(webApp.templateDir, "generated_privacy_files")), false))
		se.Router.POST("/profile/search", webApp.handleProfileSearch)
		se.Router.GET("/register", webApp.handleRegisterPage)
		se.Router.POST("/register", webApp.handleRegister)
		se.Router.GET("/validate-email", webApp.handleValidateEmail)
		se.Router.GET("/login", webApp.handleLoginPage)
		se.Router.POST("/login", webApp.handleLogin)
		se.Router.GET("/forgot-password", webApp.handleForgotPasswordPage)
		se.Router.POST("/forgot-password", webApp.handleForgotPassword)
		se.Router.GET("/reset-password", webApp.handleResetPasswordPage)
		se.Router.POST("/reset-password", webApp.handleResetPassword)
		se.Router.POST("/logout", webApp.handleLogout)
		se.Router.POST("/account/delete", webApp.handleDeleteAccount)
		se.Router.POST("/profile", webApp.handleSaveProfile)
		se.Router.POST("/profile/name", webApp.handleSaveProfileName)
		se.Router.POST("/question/save", webApp.handleSaveQuestion)
		se.Router.POST("/profile/question", webApp.handleSaveQuestion)
		se.Router.POST("/question/complete", webApp.handleCompleteQuestion)
		se.Router.GET("/user/{profileName}", webApp.handlePublicProfile)
		se.Router.POST("/user/{profileName}", webApp.handlePublicAnswer)
		se.Router.GET("/health", func(e *core.RequestEvent) error {
			return e.JSON(http.StatusOK, map[string]string{"status": "ok"})
		})
		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

func (a *App) ensureSchema(app core.App) error {
	queries := []string{
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			profile_name TEXT NOT NULL UNIQUE,
			user_status TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`, usersTableName),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			question_text TEXT NOT NULL,
			status TEXT NOT NULL,
			completed_at TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (user_id) REFERENCES %s(id) ON DELETE CASCADE
		)`, questionsTableName, usersTableName),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_questions_user_status ON %s (user_id, status)", questionsTableName),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			question_id TEXT NOT NULL,
			answer_text TEXT NOT NULL,
			delivery_status TEXT NOT NULL,
			delivery_error TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (user_id) REFERENCES %s(id) ON DELETE CASCADE,
			FOREIGN KEY (question_id) REFERENCES %s(id) ON DELETE CASCADE
		)`, responsesTableName, usersTableName, questionsTableName),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_responses_question ON %s (question_id)", responsesTableName),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TEXT NOT NULL,
			used_at TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (user_id) REFERENCES %s(id) ON DELETE CASCADE
		)`, resetTokensTableName, usersTableName),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_reset_tokens_user ON %s (user_id)", resetTokensTableName),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TEXT NOT NULL,
			used_at TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY (user_id) REFERENCES %s(id) ON DELETE CASCADE
		)`, emailValidationTokensTableName, usersTableName),
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_email_validation_tokens_user ON %s (user_id)", emailValidationTokensTableName),
	}

	for _, query := range queries {
		if _, err := app.DB().NewQuery(query).Execute(); err != nil {
			return err
		}
	}

	return nil
}

func (a *App) handleHome(e *core.RequestEvent) error {
	currentUser, _ := a.currentUser(e)
	if currentUser == nil {
		return a.render(e, http.StatusOK, "public_home.html", map[string]any{
			"Title":              "What is your Blindspot?",
			"ProfileLookupError": "",
			"ProfileLookupDraft": "",
			"CurrentUser":        nil,
		})
	}

	return a.renderDashboard(e, currentUser, map[string]any{})
}

func (a *App) handleProfileSearch(e *core.RequestEvent) error {
	profileName := strings.TrimSpace(e.Request.FormValue("profile_name"))
	user, _ := a.findUserByProfileName(profileName)
	if user != nil {
		return e.Redirect(http.StatusSeeOther, "/user/"+user.ProfileName)
	}

	currentUser, _ := a.currentUser(e)
	if currentUser == nil {
		return a.render(e, http.StatusOK, "public_home.html", map[string]any{
			"Title":              "What is your Blindspot?",
			"ProfileLookupError": "That public profile ID was not found.",
			"ProfileLookupDraft": profileName,
			"CurrentUser":        nil,
		})
	}

	return a.renderDashboard(e, currentUser, map[string]any{
		"ProfileLookupError": "That public profile ID was not found.",
		"ProfileLookupDraft": profileName,
	})
}

func (a *App) handleRegisterPage(e *core.RequestEvent) error {
	return a.render(e, http.StatusOK, "register.html", map[string]any{
		"Title":              "Register",
		"FormEmail":          "",
		"Error":              "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleAboutPage(e *core.RequestEvent) error {
	currentUser, _ := a.currentUser(e)
	return a.render(e, http.StatusOK, "about.html", map[string]any{
		"Title":              "About | What is your Blindspot?",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        currentUser,
	})
}

func (a *App) handlePrivacyPage(e *core.RequestEvent) error {
	currentUser, _ := a.currentUser(e)
	return a.render(e, http.StatusOK, "privacy.html", map[string]any{
		"Title":              "Privacy | What is your Blindspot?",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        currentUser,
	})
}

func (a *App) handleGeneratedPrivacyPage(e *core.RequestEvent) error {
	currentUser, _ := a.currentUser(e)
	content, err := a.loadGeneratedPrivacyContent()
	if err != nil {
		return e.InternalServerError("failed to load generated privacy page", err)
	}

	return a.render(e, http.StatusOK, "generated_privacy_page.html", map[string]any{
		"Title":              "Privacy Legal Terms | What is your Blindspot?",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        currentUser,
		"GeneratedPrivacy":   content,
	})
}

func (a *App) handleRegister(e *core.RequestEvent) error {
	email := strings.TrimSpace(strings.ToLower(e.Request.FormValue("email")))
	password := e.Request.FormValue("password")

	if !isValidEmail(email) {
		return a.render(e, http.StatusOK, "register.html", map[string]any{
			"Title":              "Register",
			"FormEmail":          email,
			"Error":              "Enter a valid email address to use as your user ID.",
			"ProfileLookupDraft": "",
			"ProfileLookupError": "",
			"CurrentUser":        nil,
		})
	}

	if len(password) < passwordMinLength {
		return a.render(e, http.StatusOK, "register.html", map[string]any{
			"Title":              "Register",
			"FormEmail":          email,
			"Error":              "Use a unique email and a password with at least 8 characters.",
			"ProfileLookupDraft": "",
			"ProfileLookupError": "",
			"CurrentUser":        nil,
		})
	}

	if existing, _ := a.findUserRowByEmail(email); existing != nil {
		return a.render(e, http.StatusOK, "register.html", map[string]any{
			"Title":              "Register",
			"FormEmail":          email,
			"Error":              "Use a unique email and a password with at least 8 characters.",
			"ProfileLookupDraft": "",
			"ProfileLookupError": "",
			"CurrentUser":        nil,
		})
	}

	user, err := a.createUser(email, password)
	if err != nil {
		return e.InternalServerError("failed to create user", err)
	}

	_, validationToken, tokenErr := a.createEmailValidationToken(user.ID)
	if tokenErr != nil {
		return e.InternalServerError("failed to create email validation token", tokenErr)
	}

	validationURL := buildAbsoluteURL(e, "/validate-email?token="+validationToken)
	validationNotice := fmt.Sprintf("A validation link was emailed to %s. Please validate your email to finish activating your account.", user.Email)
	validationLink := ""
	if err := a.sendEmailValidationEmail(user.Email, validationURL); err != nil {
		validationNotice = "Email delivery is not configured, so use the validation link below for local development."
		validationLink = validationURL
	}

	a.setSession(e, user.ID)
	return a.renderDashboard(e, a.userRowToView(user), map[string]any{
		"ValidationNotice": validationNotice,
		"ValidationLink":   validationLink,
	})
}

func (a *App) handleValidateEmail(e *core.RequestEvent) error {
	token := strings.TrimSpace(e.Request.URL.Query().Get("token"))
	data := map[string]any{
		"Title":              "Validate email",
		"Error":              "",
		"Success":            "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	}

	validationToken, err := a.findValidEmailValidationToken(token)
	if token == "" || err != nil {
		data["Error"] = "This validation link is invalid or has expired."
		return a.render(e, http.StatusOK, "validate_email.html", data)
	}

	if err := a.validateUserEmail(validationToken); err != nil {
		return e.InternalServerError("failed to validate email", err)
	}

	data["Success"] = "Your email has been validated. Your account is now active."
	return a.render(e, http.StatusOK, "validate_email.html", data)
}

func (a *App) handleLoginPage(e *core.RequestEvent) error {
	return a.render(e, http.StatusOK, "login.html", map[string]any{
		"Title":              "Log in",
		"FormEmail":          "",
		"Error":              "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleForgotPasswordPage(e *core.RequestEvent) error {
	return a.render(e, http.StatusOK, "forgot_password.html", map[string]any{
		"Title":              "Reset password",
		"FormEmail":          "",
		"Error":              "",
		"Success":            "",
		"ResetLink":          "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleLogin(e *core.RequestEvent) error {
	email := strings.TrimSpace(strings.ToLower(e.Request.FormValue("email")))
	password := e.Request.FormValue("password")

	user, err := a.findUserRowByEmail(email)
	if err != nil || user == nil {
		return a.render(e, http.StatusOK, "login.html", map[string]any{
			"Title":              "Log in",
			"FormEmail":          email,
			"Error":              "Incorrect email or password.",
			"ProfileLookupDraft": "",
			"ProfileLookupError": "",
			"CurrentUser":        nil,
		})
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return a.render(e, http.StatusOK, "login.html", map[string]any{
			"Title":              "Log in",
			"FormEmail":          email,
			"Error":              "Incorrect email or password.",
			"ProfileLookupDraft": "",
			"ProfileLookupError": "",
			"CurrentUser":        nil,
		})
	}

	a.setSession(e, user.ID)
	return e.Redirect(http.StatusSeeOther, "/")
}

func (a *App) handleAdminLoginPage(e *core.RequestEvent) error {
	return a.render(e, http.StatusOK, "admin_login.html", map[string]any{
		"Title":              "Admin login",
		"Error":              "",
		"FormEmail":          adminEmail,
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleAdminLogin(e *core.RequestEvent) error {
	email := strings.TrimSpace(strings.ToLower(e.Request.FormValue("email")))
	password := e.Request.FormValue("password")

	if email != adminEmail || password == "" || password != a.config.Admin.Password {
		return a.render(e, http.StatusOK, "admin_login.html", map[string]any{
			"Title":              "Admin login",
			"Error":              "Incorrect admin email or password.",
			"FormEmail":          email,
			"ProfileLookupDraft": "",
			"ProfileLookupError": "",
			"CurrentUser":        nil,
		})
	}

	a.setAdminSession(e)
	return e.Redirect(http.StatusSeeOther, "/admin-panel")
}

func (a *App) handleAdminLogout(e *core.RequestEvent) error {
	http.SetCookie(e.Response, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	return e.Redirect(http.StatusSeeOther, "/admin-panel/login")
}

func (a *App) handleAdminDashboard(e *core.RequestEvent) error {
	if !a.isAdminAuthenticated(e) {
		return e.Redirect(http.StatusSeeOther, "/admin-panel/login")
	}

	return a.render(e, http.StatusOK, "admin_dashboard.html", map[string]any{
		"Title":              "Admin | What is your Blindspot?",
		"AdminEmail":         adminEmail,
		"Users":              a.listUsers(),
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleAdminMailTestPage(e *core.RequestEvent) error {
	if !a.isAdminAuthenticated(e) {
		return e.Redirect(http.StatusSeeOther, "/admin-panel/login")
	}

	return a.render(e, http.StatusOK, "admin_mail_test.html", map[string]any{
		"Title":              "Mail test | What is your Blindspot?",
		"AdminEmail":         adminEmail,
		"EmailProvider":      a.config.EmailProvider,
		"FormEmail":          "",
		"Error":              "",
		"Success":            "",
		"TraceLogs":          []string{},
		"SMTPHost":           a.config.SMTP.Host,
		"SMTPPort":           a.config.SMTP.Port,
		"SMTPUsername":       a.config.SMTP.Username,
		"SMTPFromEmail":      a.config.SMTP.FromEmail,
		"ResendFromEmail":    a.config.Resend.FromEmail,
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleAdminMailTest(e *core.RequestEvent) error {
	if !a.isAdminAuthenticated(e) {
		return e.Redirect(http.StatusSeeOther, "/admin-panel/login")
	}

	email := strings.TrimSpace(strings.ToLower(e.Request.FormValue("email")))
	data := map[string]any{
		"Title":              "Mail test | What is your Blindspot?",
		"AdminEmail":         adminEmail,
		"EmailProvider":      a.config.EmailProvider,
		"FormEmail":          email,
		"Error":              "",
		"Success":            "",
		"TraceLogs":          []string{},
		"SMTPHost":           a.config.SMTP.Host,
		"SMTPPort":           a.config.SMTP.Port,
		"SMTPUsername":       a.config.SMTP.Username,
		"SMTPFromEmail":      a.config.SMTP.FromEmail,
		"ResendFromEmail":    a.config.Resend.FromEmail,
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	}

	if !isValidEmail(email) {
		data["Error"] = "Enter a valid email address."
		return a.render(e, http.StatusOK, "admin_mail_test.html", data)
	}

	body := strings.Join([]string{
		fmt.Sprintf("To: %s", email),
		fmt.Sprintf("From: %s", a.config.SMTP.FromEmail),
		"Subject: Blindspot SMTP test",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"This is a test email from the Blindspot SMTP diagnostics page.",
		fmt.Sprintf("Sent at: %s", time.Now().UTC().Format(time.RFC3339)),
	}, "\r\n")

	traceLogs, err := a.config.sendMailWithTrace(email, body)
	data["TraceLogs"] = traceLogs
	if err != nil {
		data["Error"] = fmt.Sprintf("SMTP test failed: %v", err)
		return a.render(e, http.StatusOK, "admin_mail_test.html", data)
	}

	data["Success"] = fmt.Sprintf("Test email accepted for delivery to %s.", email)
	return a.render(e, http.StatusOK, "admin_mail_test.html", data)
}

func (a *App) handleAdminUserDetail(e *core.RequestEvent) error {
	if !a.isAdminAuthenticated(e) {
		return e.Redirect(http.StatusSeeOther, "/admin-panel/login")
	}

	userID := e.Request.PathValue("userID")
	user, err := a.findUserRowByID(userID)
	if err != nil || user == nil {
		return e.NotFoundError("User not found", err)
	}

	return a.render(e, http.StatusOK, "admin_user_detail.html", map[string]any{
		"Title":              "Admin user detail | What is your Blindspot?",
		"AdminEmail":         adminEmail,
		"ManagedUser":        a.userRowToView(user),
		"QuestionGroups":     a.loadQuestionGroups(user.ID),
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	})
}

func (a *App) handleAdminDeleteUser(e *core.RequestEvent) error {
	if !a.isAdminAuthenticated(e) {
		return e.Redirect(http.StatusSeeOther, "/admin-panel/login")
	}

	userID := e.Request.PathValue("userID")
	if err := a.deleteUser(userID); err != nil {
		return e.InternalServerError("failed to delete user", err)
	}

	return e.Redirect(http.StatusSeeOther, "/admin-panel")
}

func (a *App) handleForgotPassword(e *core.RequestEvent) error {
	email := strings.TrimSpace(strings.ToLower(e.Request.FormValue("email")))
	data := map[string]any{
		"Title":              "Reset password",
		"FormEmail":          email,
		"Error":              "",
		"Success":            "If that email exists, a password reset link has been prepared.",
		"ResetLink":          "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	}

	if !isValidEmail(email) {
		data["Error"] = "Enter a valid email address."
		data["Success"] = ""
		return a.render(e, http.StatusOK, "forgot_password.html", data)
	}

	user, err := a.findUserRowByEmail(email)
	if err == nil && user != nil {
		_, tokenValue, tokenErr := a.createPasswordResetToken(user.ID)
		if tokenErr != nil {
			return e.InternalServerError("failed to create password reset token", tokenErr)
		}

		resetURL := buildAbsoluteURL(e, "/reset-password?token="+tokenValue)
		if err := a.sendPasswordResetEmail(user.Email, resetURL); err != nil {
			data["Success"] = "Email delivery is not configured, so use the reset link below for local development."
			data["ResetLink"] = resetURL
		} else {
			data["Success"] = fmt.Sprintf("A password reset link was emailed to %s.", user.Email)
		}
	}

	return a.render(e, http.StatusOK, "forgot_password.html", data)
}

func (a *App) handleResetPasswordPage(e *core.RequestEvent) error {
	token := strings.TrimSpace(e.Request.URL.Query().Get("token"))
	data := map[string]any{
		"Title":              "Choose a new password",
		"Token":              token,
		"Error":              "",
		"Success":            "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	}

	if token == "" {
		data["Error"] = "This reset link is invalid or incomplete."
		return a.render(e, http.StatusOK, "reset_password.html", data)
	}

	if _, err := a.findValidPasswordResetToken(token); err != nil {
		data["Error"] = "This reset link is invalid or has expired."
	}

	return a.render(e, http.StatusOK, "reset_password.html", data)
}

func (a *App) handleResetPassword(e *core.RequestEvent) error {
	token := strings.TrimSpace(e.Request.FormValue("token"))
	password := e.Request.FormValue("password")
	passwordConfirm := e.Request.FormValue("password_confirm")

	data := map[string]any{
		"Title":              "Choose a new password",
		"Token":              token,
		"Error":              "",
		"Success":            "",
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"CurrentUser":        nil,
	}

	resetToken, err := a.findValidPasswordResetToken(token)
	if err != nil {
		data["Error"] = "This reset link is invalid or has expired."
		return a.render(e, http.StatusOK, "reset_password.html", data)
	}

	if len(password) < passwordMinLength {
		data["Error"] = "Use a password with at least 8 characters."
		return a.render(e, http.StatusOK, "reset_password.html", data)
	}

	if password != passwordConfirm {
		data["Error"] = "The password confirmation does not match."
		return a.render(e, http.StatusOK, "reset_password.html", data)
	}

	if err := a.resetPassword(resetToken, password); err != nil {
		return e.InternalServerError("failed to reset password", err)
	}

	data["Success"] = "Your password has been reset. You can log in with the new password now."
	data["Token"] = ""
	return a.render(e, http.StatusOK, "reset_password.html", data)
}

func (a *App) handleLogout(e *core.RequestEvent) error {
	a.clearUserSession(e)
	return e.Redirect(http.StatusSeeOther, "/")
}

func (a *App) handleDeleteAccount(e *core.RequestEvent) error {
	currentUser, userRow := a.currentUser(e)
	if currentUser == nil || userRow == nil {
		return e.Redirect(http.StatusSeeOther, "/login")
	}

	confirmation := strings.TrimSpace(e.Request.FormValue("delete_confirmation"))
	if confirmation != "YES" {
		return a.renderDashboard(e, currentUser, map[string]any{
			"DeleteAccountError":             `Type "YES" to confirm deleting your account.`,
			"DeleteAccountConfirmationDraft": confirmation,
		})
	}

	if err := a.deleteUser(userRow.ID); err != nil {
		return e.InternalServerError("failed to delete account", err)
	}

	a.clearUserSession(e)
	return e.Redirect(http.StatusSeeOther, "/")
}

func (a *App) clearUserSession(e *core.RequestEvent) {
	http.SetCookie(e.Response, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func (a *App) handleSaveProfileName(e *core.RequestEvent) error {
	currentUser, userRow := a.currentUser(e)
	if currentUser == nil || userRow == nil {
		return e.Redirect(http.StatusSeeOther, "/login")
	}

	profileName := strings.TrimSpace(e.Request.FormValue("profile_name"))

	if profileName == "" {
		return a.renderDashboard(e, currentUser, map[string]any{
			"ProfileError":     "Your profile name is required.",
			"ProfileNameDraft": profileName,
		})
	}

	if len(profileName) > profileNameMaxLength {
		return a.renderDashboard(e, currentUser, map[string]any{
			"ProfileError":     "Your profile name must be 12 characters or fewer.",
			"ProfileNameDraft": profileName,
		})
	}

	if other, _ := a.findUserRowByProfileName(profileName); other != nil && other.ID != userRow.ID {
		return a.renderDashboard(e, currentUser, map[string]any{
			"ProfileError":     "That profile name is unavailable. Choose a different name.",
			"ProfileNameDraft": profileName,
		})
	}

	if err := a.updateUserProfile(userRow.ID, profileName); err != nil {
		return e.InternalServerError("failed to save profile", err)
	}

	refreshedRow, _ := a.findUserRowByID(userRow.ID)
	refreshedUser := a.userRowToView(refreshedRow)
	return a.renderDashboard(e, refreshedUser, map[string]any{
		"ProfileSuccess": "Your profile name has been saved.",
	})
}

func (a *App) handleSaveProfile(e *core.RequestEvent) error {
	switch strings.TrimSpace(e.Request.FormValue("profile_action")) {
	case "question":
		return a.handleSaveQuestion(e)
	case "name":
		fallthrough
	default:
		return a.handleSaveProfileName(e)
	}
}

func (a *App) handleSaveQuestion(e *core.RequestEvent) error {
	currentUser, userRow := a.currentUser(e)
	if currentUser == nil || userRow == nil {
		return e.Redirect(http.StatusSeeOther, "/login")
	}

	if currentUser.UserStatus != userStatusValidated {
		return a.renderDashboard(e, currentUser, map[string]any{
			"QuestionActionError": "Validate your email before creating a question.",
		})
	}

	questionText := strings.TrimSpace(e.Request.FormValue("profile_question"))

	if questionText == "" {
		return a.renderDashboard(e, currentUser, map[string]any{
			"QuestionActionError": "Your question is required.",
			"QuestionDraft":       questionText,
		})
	}

	if len(questionText) > questionMaxLength {
		return a.renderDashboard(e, currentUser, map[string]any{
			"QuestionActionError": "Your question must be 2000 characters or fewer.",
			"QuestionDraft":       questionText,
		})
	}

	if _, err := a.saveActiveQuestion(userRow.ID, questionText); err != nil {
		if errors.Is(err, errQuestionLocked) {
			return a.renderDashboard(e, currentUser, map[string]any{
				"QuestionActionError": "You cannot update this question after it has received responses. Complete it and create a new one instead.",
				"QuestionDraft":       currentUser.ActiveQuestion.QuestionText,
			})
		}
		return a.renderDashboard(e, currentUser, map[string]any{
			"QuestionActionError": "We could not save your question.",
			"QuestionDraft":       questionText,
		})
	}

	refreshedRow, _ := a.findUserRowByID(userRow.ID)
	refreshedUser := a.userRowToView(refreshedRow)
	return a.renderDashboard(e, refreshedUser, map[string]any{
		"QuestionActionSuccess": "Your active question has been saved.",
	})
}

func (a *App) handleCompleteQuestion(e *core.RequestEvent) error {
	currentUser, userRow := a.currentUser(e)
	if currentUser == nil || userRow == nil {
		return e.Redirect(http.StatusSeeOther, "/login")
	}

	if currentUser.ActiveQuestion == nil {
		return a.renderDashboard(e, currentUser, map[string]any{
			"QuestionActionError": "There is no active question to complete.",
		})
	}

	if err := a.completeActiveQuestion(userRow.ID); err != nil {
		return e.InternalServerError("failed to complete question", err)
	}

	refreshedRow, _ := a.findUserRowByID(userRow.ID)
	refreshedUser := a.userRowToView(refreshedRow)
	return a.renderDashboard(e, refreshedUser, map[string]any{
		"QuestionActionSuccess": "Your question has been marked as completed. You can create a new one now.",
	})
}

func (a *App) handlePublicProfile(e *core.RequestEvent) error {
	profileName := e.Request.PathValue("profileName")
	user, err := a.findUserByProfileName(profileName)
	if err != nil || user == nil {
		return e.NotFoundError("User not found", err)
	}
	return a.renderPublicProfile(e, user, map[string]any{})
}

func (a *App) handlePublicAnswer(e *core.RequestEvent) error {
	profileName := e.Request.PathValue("profileName")
	user, err := a.findUserByProfileName(profileName)
	if err != nil || user == nil {
		return e.NotFoundError("User not found", err)
	}

	answerText := strings.TrimSpace(e.Request.FormValue("answer_text"))
	if user.ActiveQuestion == nil {
		return a.renderPublicProfile(e, user, map[string]any{
			"SubmissionError": "This user has not published a question yet.",
			"AnswerDraft":     answerText,
		})
	}

	if answerText == "" || len(answerText) > answerMaxLength {
		return a.renderPublicProfile(e, user, map[string]any{
			"SubmissionError": "Answers are required and must be 4000 characters or fewer.",
			"AnswerDraft":     answerText,
		})
	}

	response, err := a.createResponse(user.ID, user.ActiveQuestion.ID, answerText)
	if err != nil {
		return e.InternalServerError("failed to save response", err)
	}

	submissionSuccess := "Your answer has been emailed to the user."
	if err := a.sendAnswerEmail(user.Email, user.ProfileName, user.ActiveQuestion.QuestionText, answerText); err != nil {
		if updateErr := a.updateResponseDelivery(response.ID, "failed", err.Error()); updateErr != nil {
			return e.InternalServerError("failed to save response delivery state", updateErr)
		}
		submissionSuccess = "Your answer was saved, but email delivery is not configured yet."
	} else {
		if updateErr := a.updateResponseDelivery(response.ID, "sent", ""); updateErr != nil {
			return e.InternalServerError("failed to save response delivery state", updateErr)
		}
	}

	return a.renderPublicProfile(e, user, map[string]any{
		"SubmissionSuccess": submissionSuccess,
	})
}

func (a *App) renderDashboard(e *core.RequestEvent, user *UserView, extras map[string]any) error {
	questionGroups := a.loadQuestionGroups(user.ID)
	questionDraft := ""
	canEditActiveQuestion := true
	if user.ActiveQuestion != nil {
		questionDraft = user.ActiveQuestion.QuestionText
		for _, group := range questionGroups {
			if group.ID == user.ActiveQuestion.ID {
				canEditActiveQuestion = len(group.Responses) == 0
				break
			}
		}
	}

	data := map[string]any{
		"Title":                          "What is your Blindspot?",
		"Subtitle":                       "Shape your public profile, manage your active question, and review incoming responses.",
		"CurrentUser":                    user,
		"QuestionGroups":                 questionGroups,
		"ProfileLookupError":             "",
		"ProfileLookupDraft":             "",
		"ProfileNameMaxLength":           profileNameMaxLength,
		"QuestionMaxLength":              questionMaxLength,
		"UserStatusValidated":            userStatusValidated,
		"ProfileError":                   "",
		"ProfileSuccess":                 "",
		"QuestionActionError":            "",
		"QuestionActionSuccess":          "",
		"ProfileNameDraft":               user.ProfileName,
		"QuestionDraft":                  questionDraft,
		"CanEditActiveQuestion":          canEditActiveQuestion,
		"ValidationNotice":               "",
		"ValidationLink":                 "",
		"DeleteAccountError":             "",
		"DeleteAccountConfirmationDraft": "",
	}
	for k, v := range extras {
		data[k] = v
	}
	return a.render(e, http.StatusOK, "home.html", data)
}

func (a *App) renderPublicProfile(e *core.RequestEvent, user *UserView, extras map[string]any) error {
	currentUser, _ := a.currentUser(e)
	data := map[string]any{
		"Title":              fmt.Sprintf("%s | What is your Blindspot?", user.ProfileName),
		"CurrentUser":        currentUser,
		"ProfileLookupDraft": "",
		"ProfileLookupError": "",
		"ProfileUser":        user,
		"ActiveQuestion":     user.ActiveQuestion,
		"AnswerMaxLength":    answerMaxLength,
		"SubmissionError":    "",
		"SubmissionSuccess":  "",
		"AnswerDraft":        "",
	}
	for k, v := range extras {
		data[k] = v
	}
	return a.render(e, http.StatusOK, "public_profile.html", data)
}

func (a *App) render(e *core.RequestEvent, status int, name string, data map[string]any) error {
	templates, err := template.ParseFiles(
		a.templateDir+"/base.html",
		a.templateDir+"/"+name,
	)
	if err != nil {
		return e.InternalServerError("failed to load templates", err)
	}

	var body bytes.Buffer
	if err := templates.ExecuteTemplate(&body, "base", data); err != nil {
		return e.InternalServerError("failed to render template", err)
	}

	e.Response.WriteHeader(status)
	_, err = body.WriteTo(e.Response)
	return err
}

func (a *App) loadGeneratedPrivacyContent() (template.HTML, error) {
	content, err := os.ReadFile(a.templateDir + "/generated_privacy.html")
	if err != nil {
		return "", err
	}

	htmlDoc := string(content)
	start := strings.Index(htmlDoc, "<div class=WordSection1>")
	end := strings.LastIndex(htmlDoc, "</div>")
	if start == -1 || end == -1 || end <= start {
		return "", fmt.Errorf("generated privacy document body not found")
	}

	body := htmlDoc[start : end+len("</div>")]
	body = strings.ReplaceAll(body, `href="generated_privacy_files/`, `href="/privacy/generated_privacy_files/`)

	return template.HTML(body), nil
}

func (a *App) currentUser(e *core.RequestEvent) (*UserView, *userRow) {
	cookie, err := e.Request.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil
	}

	userID, ok := verifySession(cookie.Value)
	if !ok {
		return nil, nil
	}

	row, err := a.findUserRowByID(userID)
	if err != nil || row == nil {
		return nil, nil
	}

	return a.userRowToView(row), row
}

func (a *App) createUser(email string, password string) (*userRow, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	row := &userRow{
		ID:           randomID(),
		Email:        email,
		PasswordHash: string(hash),
		ProfileName:  a.generateUniqueProfileName(),
		UserStatus:   userStatusUnvalidated,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		INSERT INTO %s (id, email, password_hash, profile_name, user_status, created_at)
		VALUES ({:id}, {:email}, {:password_hash}, {:profile_name}, {:user_status}, {:created_at})
	`, usersTableName)).Bind(dbx.Params{
		"id":            row.ID,
		"email":         row.Email,
		"password_hash": row.PasswordHash,
		"profile_name":  row.ProfileName,
		"user_status":   row.UserStatus,
		"created_at":    row.CreatedAt,
	}).Execute()
	if err != nil {
		return nil, err
	}

	return row, nil
}

func (a *App) updateUserProfile(userID string, profileName string) error {
	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET profile_name = {:profile_name}
		WHERE id = {:id}
	`, usersTableName)).Bind(dbx.Params{
		"id":           userID,
		"profile_name": profileName,
	}).Execute()
	return err
}

func (a *App) createPasswordResetToken(userID string) (*passwordResetTokenRow, string, error) {
	tokenValue := randomToken(24)
	now := time.Now().UTC()
	row := &passwordResetTokenRow{
		ID:        randomID(),
		UserID:    userID,
		TokenHash: hashToken(tokenValue),
		ExpiresAt: now.Add(resetTokenTTLHours * time.Hour).Format(time.RFC3339),
		UsedAt:    "",
		CreatedAt: now.Format(time.RFC3339),
	}

	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		INSERT INTO %s (id, user_id, token_hash, expires_at, used_at, created_at)
		VALUES ({:id}, {:user_id}, {:token_hash}, {:expires_at}, {:used_at}, {:created_at})
	`, resetTokensTableName)).Bind(dbx.Params{
		"id":         row.ID,
		"user_id":    row.UserID,
		"token_hash": row.TokenHash,
		"expires_at": row.ExpiresAt,
		"used_at":    row.UsedAt,
		"created_at": row.CreatedAt,
	}).Execute()
	if err != nil {
		return nil, "", err
	}

	return row, tokenValue, nil
}

func (a *App) createEmailValidationToken(userID string) (*emailValidationTokenRow, string, error) {
	tokenValue := randomToken(24)
	now := time.Now().UTC()
	row := &emailValidationTokenRow{
		ID:        randomID(),
		UserID:    userID,
		TokenHash: hashToken(tokenValue),
		ExpiresAt: now.Add(emailValidationTTLHours * time.Hour).Format(time.RFC3339),
		UsedAt:    "",
		CreatedAt: now.Format(time.RFC3339),
	}

	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		INSERT INTO %s (id, user_id, token_hash, expires_at, used_at, created_at)
		VALUES ({:id}, {:user_id}, {:token_hash}, {:expires_at}, {:used_at}, {:created_at})
	`, emailValidationTokensTableName)).Bind(dbx.Params{
		"id":         row.ID,
		"user_id":    row.UserID,
		"token_hash": row.TokenHash,
		"expires_at": row.ExpiresAt,
		"used_at":    row.UsedAt,
		"created_at": row.CreatedAt,
	}).Execute()
	if err != nil {
		return nil, "", err
	}

	return row, tokenValue, nil
}

func (a *App) findValidEmailValidationToken(rawToken string) (*emailValidationTokenRow, error) {
	if rawToken == "" {
		return nil, sql.ErrNoRows
	}

	row := &emailValidationTokenRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, user_id, token_hash, expires_at, used_at, created_at
		FROM %s
		WHERE token_hash = {:token_hash} AND used_at = ''
		LIMIT 1
	`, emailValidationTokensTableName)).Bind(dbx.Params{
		"token_hash": hashToken(rawToken),
	}).One(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	expiresAt, err := time.Parse(time.RFC3339, row.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(expiresAt) {
		return nil, sql.ErrNoRows
	}

	return row, nil
}

func (a *App) validateUserEmail(validationToken *emailValidationTokenRow) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET user_status = {:user_status}
		WHERE id = {:id}
	`, usersTableName)).Bind(dbx.Params{
		"id":          validationToken.UserID,
		"user_status": userStatusValidated,
	}).Execute()
	if err != nil {
		return err
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET used_at = {:used_at}
		WHERE user_id = {:user_id} AND used_at = ''
	`, emailValidationTokensTableName)).Bind(dbx.Params{
		"user_id": validationToken.UserID,
		"used_at": now,
	}).Execute()
	return err
}

func (a *App) findValidPasswordResetToken(rawToken string) (*passwordResetTokenRow, error) {
	if rawToken == "" {
		return nil, sql.ErrNoRows
	}

	row := &passwordResetTokenRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, user_id, token_hash, expires_at, used_at, created_at
		FROM %s
		WHERE token_hash = {:token_hash} AND used_at = ''
		LIMIT 1
	`, resetTokensTableName)).Bind(dbx.Params{
		"token_hash": hashToken(rawToken),
	}).One(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	expiresAt, err := time.Parse(time.RFC3339, row.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(expiresAt) {
		return nil, sql.ErrNoRows
	}

	return row, nil
}

func (a *App) resetPassword(resetToken *passwordResetTokenRow, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET password_hash = {:password_hash}
		WHERE id = {:id}
	`, usersTableName)).Bind(dbx.Params{
		"id":            resetToken.UserID,
		"password_hash": string(hash),
	}).Execute()
	if err != nil {
		return err
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET used_at = {:used_at}
		WHERE user_id = {:user_id} AND used_at = ''
	`, resetTokensTableName)).Bind(dbx.Params{
		"user_id": resetToken.UserID,
		"used_at": now,
	}).Execute()
	return err
}

func (a *App) findUserByProfileName(profileName string) (*UserView, error) {
	row, err := a.findUserRowByProfileName(profileName)
	if err != nil || row == nil {
		return nil, err
	}
	return a.userRowToView(row), nil
}

func (a *App) findUserRowByID(id string) (*userRow, error) {
	return a.findSingleUser("id", id)
}

func (a *App) findUserRowByEmail(email string) (*userRow, error) {
	return a.findSingleUser("email", email)
}

func (a *App) findUserRowByProfileName(profileName string) (*userRow, error) {
	return a.findSingleUser("profile_name", profileName)
}

func (a *App) listUsers() []UserView {
	rows := []userRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, email, password_hash, profile_name, user_status, created_at
		FROM %s
		ORDER BY created_at DESC
	`, usersTableName)).All(&rows)
	if err != nil {
		return []UserView{}
	}

	users := make([]UserView, 0, len(rows))
	for _, row := range rows {
		user := a.userRowToView(&row)
		if user != nil {
			users = append(users, *user)
		}
	}
	return users
}

func (a *App) deleteUser(userID string) error {
	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		DELETE FROM %s
		WHERE question_id IN (
			SELECT id FROM %s WHERE user_id = {:id}
		) OR user_id = {:id}
	`, responsesTableName, questionsTableName)).Bind(dbx.Params{
		"id": userID,
	}).Execute()
	if err != nil {
		return err
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		DELETE FROM %s
		WHERE user_id = {:id}
	`, questionsTableName)).Bind(dbx.Params{
		"id": userID,
	}).Execute()
	if err != nil {
		return err
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		DELETE FROM %s
		WHERE id = {:id}
	`, usersTableName)).Bind(dbx.Params{
		"id": userID,
	}).Execute()
	return err
}

func (a *App) findSingleUser(column string, value string) (*userRow, error) {
	row := &userRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, email, password_hash, profile_name, user_status, created_at
		FROM %s
		WHERE %s = {:value}
		LIMIT 1
	`, usersTableName, column)).Bind(dbx.Params{
		"value": value,
	}).One(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (a *App) saveActiveQuestion(userID string, questionText string) (*questionRow, error) {
	active, err := a.findActiveQuestionRow(userID)
	if err != nil {
		return nil, err
	}

	if active == nil {
		row := &questionRow{
			ID:           randomID(),
			UserID:       userID,
			QuestionText: questionText,
			Status:       questionStatusActive,
			CompletedAt:  "",
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
			INSERT INTO %s (id, user_id, question_text, status, completed_at, created_at)
			VALUES ({:id}, {:user_id}, {:question_text}, {:status}, {:completed_at}, {:created_at})
		`, questionsTableName)).Bind(dbx.Params{
			"id":            row.ID,
			"user_id":       row.UserID,
			"question_text": row.QuestionText,
			"status":        row.Status,
			"completed_at":  row.CompletedAt,
			"created_at":    row.CreatedAt,
		}).Execute()
		if err != nil {
			return nil, err
		}
		return row, nil
	}

	hasResponses, err := a.questionHasResponses(active.ID)
	if err != nil {
		return nil, err
	}
	if hasResponses {
		return nil, errQuestionLocked
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET question_text = {:question_text}
		WHERE id = {:id}
	`, questionsTableName)).Bind(dbx.Params{
		"id":            active.ID,
		"question_text": questionText,
	}).Execute()
	if err != nil {
		return nil, err
	}

	active.QuestionText = questionText
	return active, nil
}

func (a *App) questionHasResponses(questionID string) (bool, error) {
	responses, err := a.listResponsesForQuestion(questionID)
	if err != nil {
		return false, err
	}
	return len(responses) > 0, nil
}

func (a *App) completeActiveQuestion(userID string) error {
	active, err := a.findActiveQuestionRow(userID)
	if err != nil {
		return err
	}
	if active == nil {
		return nil
	}

	_, err = a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET status = {:status}, completed_at = {:completed_at}
		WHERE id = {:id}
	`, questionsTableName)).Bind(dbx.Params{
		"id":           active.ID,
		"status":       questionStatusDone,
		"completed_at": time.Now().UTC().Format(time.RFC3339),
	}).Execute()
	return err
}

func (a *App) findActiveQuestionRow(userID string) (*questionRow, error) {
	row := &questionRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, user_id, question_text, status, completed_at, created_at
		FROM %s
		WHERE user_id = {:user_id} AND status = {:status}
		ORDER BY created_at DESC
		LIMIT 1
	`, questionsTableName)).Bind(dbx.Params{
		"user_id": userID,
		"status":  questionStatusActive,
	}).One(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (a *App) listQuestionRowsForUser(userID string) ([]questionRow, error) {
	rows := []questionRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, user_id, question_text, status, completed_at, created_at
		FROM %s
		WHERE user_id = {:user_id}
		ORDER BY CASE WHEN status = 'active' THEN 0 ELSE 1 END, created_at DESC
	`, questionsTableName)).Bind(dbx.Params{
		"user_id": userID,
	}).All(&rows)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (a *App) createResponse(userID string, questionID string, answerText string) (*responseRow, error) {
	row := &responseRow{
		ID:             randomID(),
		UserID:         userID,
		QuestionID:     questionID,
		AnswerText:     answerText,
		DeliveryStatus: "pending",
		DeliveryError:  "",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		INSERT INTO %s (id, user_id, question_id, answer_text, delivery_status, delivery_error, created_at)
		VALUES ({:id}, {:user_id}, {:question_id}, {:answer_text}, {:delivery_status}, {:delivery_error}, {:created_at})
	`, responsesTableName)).Bind(dbx.Params{
		"id":              row.ID,
		"user_id":         row.UserID,
		"question_id":     row.QuestionID,
		"answer_text":     row.AnswerText,
		"delivery_status": row.DeliveryStatus,
		"delivery_error":  row.DeliveryError,
		"created_at":      row.CreatedAt,
	}).Execute()
	if err != nil {
		return nil, err
	}

	return row, nil
}

func (a *App) updateResponseDelivery(responseID string, status string, deliveryError string) error {
	_, err := a.pb.DB().NewQuery(fmt.Sprintf(`
		UPDATE %s
		SET delivery_status = {:delivery_status}, delivery_error = {:delivery_error}
		WHERE id = {:id}
	`, responsesTableName)).Bind(dbx.Params{
		"id":              responseID,
		"delivery_status": status,
		"delivery_error":  deliveryError,
	}).Execute()
	return err
}

func (a *App) listResponsesForQuestion(questionID string) ([]responseRow, error) {
	rows := []responseRow{}
	err := a.pb.DB().NewQuery(fmt.Sprintf(`
		SELECT id, user_id, question_id, answer_text, delivery_status, delivery_error, created_at
		FROM %s
		WHERE question_id = {:question_id}
		ORDER BY created_at DESC
	`, responsesTableName)).Bind(dbx.Params{
		"question_id": questionID,
	}).All(&rows)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (a *App) userRowToView(row *userRow) *UserView {
	if row == nil {
		return nil
	}

	user := &UserView{
		ID:          row.ID,
		Email:       row.Email,
		ProfileName: row.ProfileName,
		UserStatus:  row.UserStatus,
	}

	if active, err := a.findActiveQuestionRow(row.ID); err == nil && active != nil {
		user.ActiveQuestion = questionRowToView(active)
	}

	return user
}

func questionRowToView(row *questionRow) *QuestionView {
	if row == nil {
		return nil
	}

	return &QuestionView{
		ID:           row.ID,
		QuestionText: row.QuestionText,
		Status:       row.Status,
		Created:      row.CreatedAt,
		CompletedAt:  row.CompletedAt,
	}
}

func responseRowToView(row responseRow) ResponseView {
	return ResponseView{
		ID:             row.ID,
		AnswerText:     row.AnswerText,
		DeliveryStatus: row.DeliveryStatus,
		DeliveryError:  row.DeliveryError,
		Created:        row.CreatedAt,
	}
}

func (a *App) loadQuestionGroups(userID string) []QuestionGroupView {
	questionRows, err := a.listQuestionRowsForUser(userID)
	if err != nil {
		return []QuestionGroupView{}
	}

	groups := make([]QuestionGroupView, 0, len(questionRows))
	for _, row := range questionRows {
		responseRows, err := a.listResponsesForQuestion(row.ID)
		if err != nil {
			responseRows = []responseRow{}
		}

		responses := make([]ResponseView, 0, len(responseRows))
		for _, response := range responseRows {
			responses = append(responses, responseRowToView(response))
		}

		groups = append(groups, QuestionGroupView{
			QuestionView: *questionRowToView(&row),
			Responses:    responses,
		})
	}

	return groups
}

func isValidEmail(email string) bool {
	if len(email) < 3 || !strings.Contains(email, "@") {
		return false
	}
	parts := strings.Split(email, "@")
	return len(parts) == 2 && parts[0] != "" && strings.Contains(parts[1], ".")
}

func (a *App) generateUniqueProfileName() string {
	for {
		buf := make([]byte, profileNameMaxLength)
		randomBytes := make([]byte, profileNameMaxLength)
		if _, err := rand.Read(randomBytes); err != nil {
			panic(err)
		}
		for i := range buf {
			buf[i] = profileNameAlphabet[int(randomBytes[i])%len(profileNameAlphabet)]
		}

		candidate := string(buf)
		if existing, _ := a.findUserRowByProfileName(candidate); existing == nil {
			return candidate
		}
	}
}

func randomID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func randomToken(byteCount int) string {
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func buildAbsoluteURL(e *core.RequestEvent, path string) string {
	scheme := "http"
	if e.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + e.Request.Host + path
}

func signedValue(raw string) string {
	secret := []byte(appConfig.SessionSecret)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(raw))
	signature := hex.EncodeToString(mac.Sum(nil))
	return raw + "|" + signature
}

func verifiedSignedValue(value string) (string, bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 2 {
		return "", false
	}

	expected := signedValue(parts[0])
	if !hmac.Equal([]byte(expected), []byte(value)) {
		return "", false
	}

	return parts[0], true
}

func setSessionValue(userID string) string {
	return signedValue(userID)
}

func verifySession(value string) (string, bool) {
	return verifiedSignedValue(value)
}

func (a *App) setSession(e *core.RequestEvent, userID string) {
	http.SetCookie(e.Response, &http.Cookie{
		Name:     sessionCookieName,
		Value:    setSessionValue(userID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) setAdminSession(e *core.RequestEvent) {
	http.SetCookie(e.Response, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    signedValue(adminEmail),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) isAdminAuthenticated(e *core.RequestEvent) bool {
	cookie, err := e.Request.Cookie(adminSessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}

	value, ok := verifiedSignedValue(cookie.Value)
	return ok && value == adminEmail
}

func (a *App) sendAnswerEmail(recipientEmail string, profileName string, profileQuestion string, answerText string) error {
	if _, err := a.config.sendMailWithTrace(recipientEmail, ""); err != nil && strings.Contains(err.Error(), "not configured") {
		return fmt.Errorf("email delivery is not configured")
	}

	body := strings.Join([]string{
		"To: " + recipientEmail,
		"Subject: New answer for " + profileName,
		"MIME-version: 1.0",
		"Content-Type: text/plain; charset=\"UTF-8\"",
		"",
		"You received a new answer for /user/" + profileName + ".",
		"",
		"Your question:",
		profileQuestion,
		"",
		"Submitted by: Anonymous visitor",
		"",
		"Answer:",
		answerText,
	}, "\r\n")

	return a.config.sendMail(recipientEmail, body)
}

func (a *App) sendPasswordResetEmail(recipientEmail string, resetURL string) error {
	if _, err := a.config.sendMailWithTrace(recipientEmail, ""); err != nil && strings.Contains(err.Error(), "not configured") {
		return fmt.Errorf("email delivery is not configured")
	}

	body := strings.Join([]string{
		"To: " + recipientEmail,
		"Subject: Reset your password",
		"MIME-version: 1.0",
		"Content-Type: text/plain; charset=\"UTF-8\"",
		"",
		"Use the link below to choose a new password:",
		resetURL,
		"",
		fmt.Sprintf("This link expires in %d hours.", resetTokenTTLHours),
	}, "\r\n")

	return a.config.sendMail(recipientEmail, body)
}

func (a *App) sendEmailValidationEmail(recipientEmail string, validationURL string) error {
	if _, err := a.config.sendMailWithTrace(recipientEmail, ""); err != nil && strings.Contains(err.Error(), "not configured") {
		return fmt.Errorf("email delivery is not configured")
	}

	body := strings.Join([]string{
		"To: " + recipientEmail,
		"Subject: Validate your email",
		"MIME-version: 1.0",
		"Content-Type: text/plain; charset=\"UTF-8\"",
		"",
		"Welcome to Blindspot.",
		"",
		"Use the link below to validate your email address:",
		validationURL,
		"",
		fmt.Sprintf("This link expires in %d hours.", emailValidationTTLHours),
	}, "\r\n")

	return a.config.sendMail(recipientEmail, body)
}
