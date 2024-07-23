package handler

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/abyan.dev/auth/pkg/model"
	"github.com/abyan.dev/auth/pkg/response"
	"github.com/abyan.dev/auth/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"gopkg.in/gomail.v2"
	"gorm.io/gorm"
)

type RequestRegistrationPayload struct {
	Email string `json:"email"`
}

type VerifyRegistrationPayload struct {
	Name            string `json:"name"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

type LoginPayload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func RequestRegistration(c *fiber.Ctx) error {
	config, err := utils.LoadMailEnv()
	if err != nil {
		return response.InternalServerError(c, err.Error())
	}

	frontendUrl := os.Getenv("FRONTEND_URL")
	if frontendUrl == "" {
		return response.InternalServerError(c, "FRONTEND_URL environment variable is not set.")
	}

	requestPayload := RequestRegistrationPayload{}

	if err := c.BodyParser(&requestPayload); err != nil {
		return response.BadRequest(c, "Invalid request body.")
	}

	isEmailValid, emailValFeedback := utils.ValidateEmail(requestPayload.Email)
	if !isEmailValid {
		return response.BadRequest(c, emailValFeedback)
	}

	htmlBody, err := os.ReadFile("templates/email-verification.html")
	if err != nil {
		return response.InternalServerError(c, "Failed to load email template.")
	}

	token, err := utils.CreateJWT(requestPayload.Email, "new user", "user", 10)
	if err != nil {
		return response.InternalServerError(c, "Failed to create JWT.")
	}

	verificationLink := fmt.Sprintf(frontendUrl+"/verify-email?token=%s", token)

	tmpl, err := template.New("email").Parse(string(htmlBody))
	if err != nil {
		return response.InternalServerError(c, "Failed to parse email template.")
	}

	data := struct {
		Email            string
		VerificationLink string
	}{
		Email:            requestPayload.Email,
		VerificationLink: verificationLink,
	}

	var emailBody strings.Builder
	if err := tmpl.Execute(&emailBody, data); err != nil {
		return response.InternalServerError(c, "Failed to execute email template.")
	}

	m := gomail.NewMessage()
	m.SetHeader("From", config.SenderEmail)
	m.SetHeader("To", requestPayload.Email)
	m.SetHeader("Subject", "Email Verification")
	m.SetBody("text/html", emailBody.String())

	port, err := strconv.Atoi(config.SmtpPort)
	if err != nil {
		return response.InternalServerError(c, "Invalid SMTP_PORT value.")
	}
	d := gomail.NewDialer(config.SmtpHost, port, config.SmtpUser, config.SmtpPass)

	if err := d.DialAndSend(m); err != nil {
		return response.InternalServerError(c, "Failed to send confirmation email to user.")
	}

	return response.Ok(c, "Successfully sent a confirmation email to user.")
}

func VerifyRegistration(c *fiber.Ctx) error {
	token := c.Locals("user").(*jwt.Token)
	claims := token.Claims.(jwt.MapClaims)
	email := claims["email"].(string)

	requestPayload := VerifyRegistrationPayload{}

	if err := c.BodyParser(&requestPayload); err != nil {
		return response.BadRequest(c, "Invalid request body.")
	}

	if requestPayload.Password != requestPayload.ConfirmPassword {
		return response.BadRequest(c, "Passwords do not match.")
	}

	isEmailValid, emailValFeedback := utils.ValidateEmail(email)
	if !isEmailValid {
		return response.BadRequest(c, emailValFeedback)
	}

	isNameValid, nameValFeedback := utils.ValidateName(requestPayload.Name)
	if !isNameValid {
		return response.BadRequest(c, nameValFeedback)
	}

	isPasswordValid, passwordValFeedback := utils.ValidatePassword(requestPayload.Password)
	if !isPasswordValid {
		return response.BadRequest(c, passwordValFeedback)
	}

	db := c.Locals("db").(*gorm.DB)

	var existingUser model.User
	if err := db.Where("email = ?", email).First(&existingUser).Error; err == nil {
		return response.BadRequest(c, "Email already exists.")
	}

	hashedPassword, err := utils.HashPassword(requestPayload.Password)
	if err != nil {
		return response.InternalServerError(c, "Failed to hash password.")
	}

	user := model.User{
		Name:     requestPayload.Name,
		Email:    email,
		Password: hashedPassword,
		Role:     "user",
	}

	if err := db.Create(&user).Error; err != nil {
		return response.InternalServerError(c, "Something went wrong. Please try again later.")
	}

	authTokenPair, err := utils.CreateAuthTokenPair(c, email, requestPayload.Name, "user")
	if err != nil {
		return response.InternalServerError(c, "Failed to create authentication tokens.")
	}

	accessCookie := utils.CreateSecureCookie("access_token", authTokenPair.AccessToken, 5*time.Minute)
	refreshCookie := utils.CreateSecureCookie("refresh_token", authTokenPair.RefreshToken, 7*24*time.Hour)

	c.Cookie(accessCookie)
	c.Cookie(refreshCookie)

	data := AuthResponse{
		AccessToken:  authTokenPair.AccessToken,
		RefreshToken: authTokenPair.RefreshToken,
	}

	return response.Ok(c, "Successfully verified and registered user.", data)
}

func Login(c *fiber.Ctx) error {
	requestPayload := LoginPayload{}

	if err := c.BodyParser(&requestPayload); err != nil {
		return response.BadRequest(c, "Invalid request body.")
	}

	isEmailValid, emailValFeedback := utils.ValidateEmail(requestPayload.Email)
	if !isEmailValid {
		return response.BadRequest(c, emailValFeedback)
	}

	isPasswordValid, passwordValFeedback := utils.ValidatePassword(requestPayload.Password)
	if !isPasswordValid {
		return response.BadRequest(c, passwordValFeedback)
	}

	db := c.Locals("db").(*gorm.DB)

	var user model.User
	if err := db.Where("email = ?", requestPayload.Email).First(&user).Error; err != nil {
		return response.BadRequest(c, "Invalid email or password.")
	}

	if !utils.CheckPasswordHash(requestPayload.Password, user.Password) {
		return response.BadRequest(c, "Invalid email or password.")
	}

	authTokenPair, err := utils.CreateAuthTokenPair(c, user.Email, user.Name, user.Role)
	if err != nil {
		return response.InternalServerError(c, "Failed to create JWT tokens.")
	}

	accessCookie := utils.CreateSecureCookie("access_token", authTokenPair.AccessToken, 5*time.Minute)
	refreshCookie := utils.CreateSecureCookie("refresh_token", authTokenPair.RefreshToken, 7*24*time.Hour)

	c.Cookie(accessCookie)
	c.Cookie(refreshCookie)

	data := AuthResponse{
		AccessToken:  authTokenPair.AccessToken,
		RefreshToken: authTokenPair.RefreshToken,
	}

	return response.Ok(c, "Successfully logged user in.", data)
}

func Logout(c *fiber.Ctx) error {
	expiredAccessCookie := utils.InvalidateCookie("access_token")
	expiredRefreshCookie := utils.InvalidateCookie("refresh_token")
	c.Cookie(expiredAccessCookie)
	c.Cookie(expiredRefreshCookie)
	return response.Ok(c, "Successfully logged user out.")
}
