// Package tokenizer runs the pinned offline Hugging Face tokenizer behind a typed port.
package tokenizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/The-Vibe-Company/quivr-v2/internal/content"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/The-Vibe-Company/quivr-v2/internal/processing"
)

type Config struct {
	Python string `json:"python"`
	Script string `json:"script"`
	Model  string `json:"model"`
}
type Encoder struct{ Config Config }

func (e Encoder) Encode(ctx context.Context, input []processing.TokenInput) ([]processing.Encoding, error) {
	if len(input) > 512 {
		return nil, processing.ErrUnsupported
	}
	b, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if len(b) > processing.Parameters.MaxSerializedBatchBytes {
		return nil, processing.ErrUnsupported
	}
	rawBytes := 0
	for _, item := range input {
		rawBytes += len(item.Text)
	}
	if rawBytes > processing.Parameters.MaxModelBatchBytes {
		return nil, processing.ErrUnsupported
	}
	script, err := os.ReadFile(e.Config.Script)
	if err != nil || content.Hash(script) != processing.Parameters.TokenizerScriptSHA256 {
		return nil, errors.New("tokenizer script checksum mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Config.Python, "-c", string(script), e.Config.Model)
	cmd.Stdin = bytes.NewReader(b)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Never include subprocess output in errors: it may contain source content.
	if err = cmd.Start(); err != nil {
		return nil, errors.New("tokenizer unavailable")
	}
	output, readErr := io.ReadAll(io.LimitReader(pipe, 16<<20))
	err = cmd.Wait()
	if err != nil || readErr != nil {
		return nil, errors.New("tokenizer unavailable")
	}
	var encodings []processing.Encoding
	if err = json.Unmarshal(output, &encodings); err != nil || len(encodings) != len(input) {
		return nil, errors.New("invalid tokenizer response")
	}
	return encodings, nil
}
