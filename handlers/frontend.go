package handlers

import (
	"encoding/base64"
	"html/template"
	"os"

	"github.com/gofiber/fiber/v2"
)

type FrontendHandler struct {
	templates *template.Template
	ordfsName string
}

type PageData struct {
	OrdfsName string
}

type PreviewData struct {
	HtmlData template.HTML
}

func NewFrontendHandler() (*FrontendHandler, error) {
	tmpl := template.New("")

	// Parse all template files including nested directories
	patterns := []string{
		"frontend/pages/*.html",
		"frontend/partials/*.html",
		"frontend/partials/modals/*.html",
	}

	for _, pattern := range patterns {
		_, err := tmpl.ParseGlob(pattern)
		if err != nil {
			return nil, err
		}
	}

	ordfsName := os.Getenv("ORDFS_NAME")
	if ordfsName == "" {
		ordfsName = ""
	}

	return &FrontendHandler{
		templates: tmpl,
		ordfsName: ordfsName,
	}, nil
}

func (h *FrontendHandler) RenderIndex(c *fiber.Ctx) error {
	data := PageData{
		OrdfsName: h.ordfsName,
	}

	c.Set("Content-Type", "text/html; charset=utf-8")
	return h.templates.ExecuteTemplate(c, "index.html", data)
}

func (h *FrontendHandler) RenderPreview(c *fiber.Ctx) error {
	b64Html := c.Params("b64HtmlData")
	if b64Html == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Missing base64 HTML data")
	}

	htmlBytes, err := base64.StdEncoding.DecodeString(b64Html)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid base64 data")
	}

	data := PreviewData{
		HtmlData: template.HTML(htmlBytes),
	}

	c.Set("Content-Type", "text/html; charset=utf-8")
	return h.templates.ExecuteTemplate(c, "preview.html", data)
}

func (h *FrontendHandler) RenderPreviewPost(c *fiber.Ctx) error {
	body := c.Body()
	if len(body) == 0 {
		return c.Status(fiber.StatusBadRequest).SendString("Missing request body")
	}

	contentType := c.Get("Content-Type")
	if contentType != "" {
		c.Set("Content-Type", contentType)
	}
	return c.Send(body)
}

func (h *FrontendHandler) Render404(c *fiber.Ctx) error {
	data := PageData{
		OrdfsName: h.ordfsName,
	}

	c.Status(fiber.StatusNotFound)
	c.Set("Content-Type", "text/html; charset=utf-8")
	return h.templates.ExecuteTemplate(c, "404.html", data)
}
