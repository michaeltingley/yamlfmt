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

package hotfix

import (
	"bufio"
	"bytes"
	"context"
	"regexp"
	"strings"

	"github.com/google/yamlfmt"
)

// Placeholders can survive into the emitter output when a block scalar that
// contained a blank line is re-emitted as a flow scalar (the line-based scan
// in restoreLineBreakFeature then can't see them). Two shapes occur:
//   - double-quoted: `...\n   #magic...\n...` — restore the blank line by
//     collapsing to `\n` (the following `\n` is already present);
//   - single-quoted / plain folded: `... #magic... next` — the emitter has
//     already folded the surrounding newlines to spaces, so just drop the
//     marker and normalise to a single space.
//
// See google/yamlfmt#280.
var (
	quotedPlaceholderRe = regexp.MustCompile(`\\n *` + regexp.QuoteMeta(lineBreakPlaceholder))
	foldedPlaceholderRe = regexp.MustCompile(` *` + regexp.QuoteMeta(lineBreakPlaceholder) + ` *`)
)

const lineBreakPlaceholder = "#magic___^_^___line"

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
	return func(_ context.Context, content []byte) (context.Context, []byte, error) {
		var buf bytes.Buffer
		reader := bytes.NewReader(content)
		scanner := bufio.NewScanner(reader)
		var inLineBreaks bool
		var padding paddinger
		for scanner.Scan() {
			txt := scanner.Text()
			if strings.TrimSpace(txt) == "" { // line break or empty space line.
				if chomp && inLineBreaks {
					continue
				}
				buf.WriteString(padding.String()) // prepend some padding incase literal multiline strings.
				buf.WriteString(lineBreakPlaceholder)
				buf.WriteString(newlineStr)
				inLineBreaks = true
			} else {
				padding.adjust(txt)
				buf.WriteString(txt)
				buf.WriteString(newlineStr)
				inLineBreaks = false
			}
		}
		return nil, buf.Bytes(), scanner.Err()
	}
}

func restoreLineBreakFeature(newlineStr string) yamlfmt.FeatureFunc {
	return func(_ context.Context, content []byte) (context.Context, []byte, error) {
		var buf bytes.Buffer
		reader := bytes.NewReader(content)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			txt := scanner.Text()
			if strings.TrimSpace(txt) == "" {
				// The basic yaml lib inserts newline when there is a comment(either placeholder or by user)
				// followed by optional line breaks and a `---` multi-documents.
				// To fix it, the empty line could only be inserted by us.
				continue
			}
			if strings.HasPrefix(strings.TrimLeft(txt, " "), lineBreakPlaceholder) {
				buf.WriteString(newlineStr)
				continue
			}
			if strings.Contains(txt, lineBreakPlaceholder) {
				// Placeholder survived inside a flow/quoted scalar on this
				// line; strip it without leaking the sentinel into output.
				txt = quotedPlaceholderRe.ReplaceAllString(txt, `\n`)
				txt = foldedPlaceholderRe.ReplaceAllString(txt, " ")
			}
			buf.WriteString(txt)
			buf.WriteString(newlineStr)
		}
		return nil, buf.Bytes(), scanner.Err()
	}
}
