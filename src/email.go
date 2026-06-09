package main

import (
	"fmt"
	"log"
	"net/smtp"

	"discord-music-bot/src/config"
)

// SendAdminUploadNotification formats and sends an HTML email to the
// administrator when a new custom song upload needs approval.
func SendAdminUploadNotification(cfg *config.Config, uploadID int, title, artist, uploader string) error {
	if cfg.SMTPHost == "" || cfg.AdminEmail == "" {
		log.Printf("SMTP not configured — skipping admin notification for upload %d", uploadID)
		return nil
	}

	subject := fmt.Sprintf("Subject: [Music Bot] Approval Required: %s - %s\r\n", title, artist)
	fromHeader := fmt.Sprintf("From: %s\r\n", cfg.SMTPFrom)
	toHeader := fmt.Sprintf("To: %s\r\n", cfg.AdminEmail)
	mime := "MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n"

	body := fmt.Sprintf(`
<html>
<body style="font-family:Arial,sans-serif;color:#333;line-height:1.6">
  <div style="max-width:600px;padding:20px;border:1px solid #ddd;border-radius:5px">
    <h2 style="color:#7289da;border-bottom:2px solid #7289da;padding-bottom:10px">
      Discord Music Bot — New Upload
    </h2>
    <p>A new custom audio track is awaiting your review.</p>
    <table style="width:100%%;background:#f9f9f9;padding:15px;border-radius:5px">
      <tr><td><strong>ID:</strong></td><td>%d</td></tr>
      <tr><td><strong>Title:</strong></td><td>%s</td></tr>
      <tr><td><strong>Artist:</strong></td><td>%s</td></tr>
      <tr><td><strong>Uploaded By:</strong></td><td>%s</td></tr>
    </table>
    <p style="margin-top:20px">Use these Discord commands to review:</p>
    <pre style="background:#2c2f33;color:#fff;padding:15px;border-radius:5px">
<span style="color:#43b581">/approve %d</span>  — Approve and move to library
<span style="color:#f04747">/reject  %d</span>  — Reject and delete file</pre>
  </div>
</body>
</html>`, uploadID, title, artist, uploader, uploadID, uploadID)

	msg := []byte(fromHeader + toHeader + subject + mime + body)
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)

	// Use PlainAuth only when credentials are provided.
	var auth smtp.Auth
	if cfg.SMTPUsername != "" && cfg.SMTPPassword != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPHost)
	}

	if err := smtp.SendMail(addr, auth, cfg.SMTPFrom, []string{cfg.AdminEmail}, msg); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}

	log.Printf("Admin notification sent for upload ID %d → %s", uploadID, cfg.AdminEmail)
	return nil
}
