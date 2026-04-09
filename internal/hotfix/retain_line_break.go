// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The features in this file are to retain line breaks.
// The basic idea is to insert/remove placeholder comments in the yaml document before and after the format process.
//
// A blank line inside a multi-line scalar (|, >, "...", '...', or plain) is
// content, not structural whitespace. The original implementation inserted the
// placeholder for every blank line, which corrupted such scalars
// (google/yamlfmt#280, #86). To avoid that, both actions first run the YAML
// scanner over their input and skip the rewrite for any line that falls inside
// a multi-line scalar token. The scanner is authoritative, so this covers all
// scalar styles without lexical heuristics.

package hotfix

import (
	"bufio"
	"bytes"
	"context"
	"strings"

	"github.com/google/yamlfmt"
	"github.com/google/yamlfmt/pkg/yaml"
)

const lineBreakPlaceholder = "#magic___^_^___line"

// ctxInputHadProtectedBlank carries whether the BeforeAction passed through
// any blank line as scalar content. When false, the encoder output cannot
// contain blank lines that are scalar content (the encoder preserves scalar
// style, so a single-line input scalar stays single-line in output, and no
// blank-in-scalar was carried through from a multi-line one), and the
// AfterAction can skip its own scan.
type ctxInputHadProtectedBlank struct{}

// scalarContentLines returns the set of 0-indexed line numbers that fall
// strictly inside a multi-line scalar token in src. "Strictly inside" is the
// open interval (start_mark.line, end_mark.line): the start line carries the
// scalar's opening syntax (`|`, `>`, opening quote, or first plain word) and
// the end line is either the closing quote / last plain word or, for block
// scalars, the first line after the scanner finished consuming trailing
// breaks. Neither boundary line is itself a wholly-blank content line, so the
// open interval is exactly the set of lines whose blankness is scalar content
// rather than structure.
func scalarContentLines(src []byte) map[int]struct{} {
	in := make(map[int]struct{})
	for _, r := range yaml.ScanMultilineScalarRanges(src) {
		for l := r.StartLine + 1; l < r.EndLine; l++ {
			in[l] = struct{}{}
		}
	}
	return in
}

type paddinger struct {
	strings.Builder
}

func (p *paddinger) adjust(txt string) {
	var indentSize int
	for i := 0; i < len(txt) && txt[i] == ' '; i++ { // yaml only allows space to indent.
		indentSize++
	}
	// Track the indent of the most recent line, not the max ever seen: a
	// grow-only padding over-indents the placeholder after any deeper-nested
	// line, which inside a block scalar becomes leading-whitespace content and
	// can force the emitter to a quoted style. See google/yamlfmt#280.
	if indentSize == p.Len() {
		return
	}
	p.Reset()
	for i := 0; i < indentSize; i++ {
		p.WriteByte(' ')
	}
}

func MakeFeatureRetainLineBreak(linebreakStr string, chomp bool) yamlfmt.Feature {
	return yamlfmt.Feature{
		Name:         "Retain Line Breaks",
		BeforeAction: replaceLineBreakFeature(linebreakStr, chomp),
		AfterAction:  restoreLineBreakFeature(linebreakStr),
	}
}

func replaceLineBreakFeature(newlineStr string, chomp bool) yamlfmt.FeatureFunc {
	return func(ctx context.Context, content []byte) (context.Context, []byte, error) {
		inScalar := scalarContentLines(content)
		var hadProtectedBlank bool
		var buf bytes.Buffer
		scanner := bufio.NewScanner(bytes.NewReader(content))
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		var lineNo int
		var inLineBreaks bool
		var padding paddinger
		for scanner.Scan() {
			txt := scanner.Text()
			_, protected := inScalar[lineNo]
			lineNo++
			if strings.TrimRight(txt, " \t") == "" {
				if protected {
					// Blank (or whitespace-only) line that is scalar content:
					// pass through verbatim so the decoder sees the original
					// value. The encoder preserves it natively. This is not a
					// structural blank, so leave the chomp state untouched.
					hadProtectedBlank = true
					buf.WriteString(txt)
					buf.WriteString(newlineStr)
					continue
				}
				if chomp && inLineBreaks {
					continue
				}
				buf.WriteString(padding.String())
				buf.WriteString(lineBreakPlaceholder)
				buf.WriteString(newlineStr)
				inLineBreaks = true
				continue
			}
			padding.adjust(txt)
			buf.WriteString(txt)
			buf.WriteString(newlineStr)
			inLineBreaks = false
		}
		ctx = context.WithValue(ctx, ctxInputHadProtectedBlank{}, hadProtectedBlank)
		return ctx, buf.Bytes(), scanner.Err()
	}
}

func restoreLineBreakFeature(newlineStr string) yamlfmt.FeatureFunc {
	return func(ctx context.Context, content []byte) (context.Context, []byte, error) {
		// The output-side scan is only needed when the BeforeAction passed a
		// blank line through as scalar content: that's the only way the
		// encoder output can have a blank line that is scalar content rather
		// than structure (the encoder preserves scalar style, so it does not
		// introduce blank-in-scalar from a value that didn't carry one in).
		// When BeforeAction never took that branch, fall through with a nil
		// map (every lookup misses), matching the original v0.21.0 fast path.
		var inScalar map[int]struct{}
		if had, _ := ctx.Value(ctxInputHadProtectedBlank{}).(bool); had {
			inScalar = scalarContentLines(content)
		}
		var buf bytes.Buffer
		scanner := bufio.NewScanner(bytes.NewReader(content))
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		var lineNo int
		for scanner.Scan() {
			txt := scanner.Text()
			_, protected := inScalar[lineNo]
			lineNo++
			if txt == "" {
				if protected {
					buf.WriteString(newlineStr)
					continue
				}
				// Structural blank in encoder output that we did not request
				// via a placeholder: drop it (matches the original hotfix's
				// handling of the comment-before-`---` quirk).
				continue
			}
			if strings.HasPrefix(strings.TrimLeft(txt, " "), lineBreakPlaceholder) {
				if protected {
					// The placeholder text appears inside a scalar in the
					// output. The BeforeAction never inserts placeholders into
					// scalar content, so this can only be literal user content
					// that happens to match the sentinel. Round-trip it.
					buf.WriteString(txt)
					buf.WriteString(newlineStr)
					continue
				}
				buf.WriteString(newlineStr)
				continue
			}
			buf.WriteString(txt)
			buf.WriteString(newlineStr)
		}
		return nil, buf.Bytes(), scanner.Err()
	}
}
