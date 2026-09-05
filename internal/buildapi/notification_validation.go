package buildapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	MaxOperationRequestBytes = 4 * 1024 * 1024
	MaxCallbackURLBytes      = 2048
	MaxExternalIDBytes       = 512
)

func validBoundedText(value string, maximum int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func validateOperationMetadata(externalID string, callback *BuildCallback) error {
	if !validBoundedText(externalID, MaxExternalIDBytes, true) {
		return errors.New("externalId exceeds its size limit or contains control characters")
	}
	if callback == nil {
		return nil
	}
	u, err := url.Parse(callback.URL)
	if err != nil || !validBoundedText(callback.URL, MaxCallbackURLBytes, false) || strings.Contains(callback.URL, " ") ||
		u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("callback.url must be an HTTPS URL without userinfo or a fragment")
	}
	if len(callback.Secret) > base64.StdEncoding.EncodedLen(4096) {
		return errors.New("callback.secret exceeds its size limit")
	}
	secret, err := base64.StdEncoding.Strict().DecodeString(callback.Secret)
	if err != nil || len(secret) < 32 || len(secret) > 4096 {
		return errors.New("callback.secret must encode 32 to 4096 random bytes as base64")
	}
	return nil
}

func bindOperationRequest(c *gin.Context, req any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, MaxOperationRequestBytes))
	if err == nil {
		err = json.Unmarshal(body, req)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "operation request exceeds 4 MiB", "code": "RequestTooLarge"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON request", "code": "InvalidRequest"})
		}
		return false
	}
	return true
}
