/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/*
 * This algorithm is updated based on code located at:
 * http://members.unine.ch/jacques.savoy/clef/
 *
 * Full copyright for that code follows:
 */

/*
 * Copyright (c) 2005, Jacques Savoy
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions are met:
 *
 * Redistributions of source code must retain the above copyright notice, this
 * list of conditions and the following disclaimer. Redistributions in binary
 * form must reproduce the above copyright notice, this list of conditions and
 * the following disclaimer in the documentation and/or other materials
 * provided with the distribution. Neither the name of the author nor the names
 * of its contributors may be used to endorse or promote products derived from
 * this software without specific prior written permission.
 *
 * THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
 * AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
 * LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
 * CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
 * SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
 * INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
 * CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
 * ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
 * POSSIBILITY OF SUCH DAMAGE.
 */

// Go adaptation of Apache Lucene FrenchLightStemmer (UniNE algorithm).
package content

import (
	"golang.org/x/text/unicode/norm"
	"strings"
	"unicode"
)

var frenchLigatures = strings.NewReplacer("œ", "oe", "æ", "ae")

// AnalyzeKeywords leaves source text unchanged unless the configured analyzer
// asks for a separate French keyword copy. Both ingest and query use this path.
func AnalyzeKeywords(text, analyzer string) string {
	if analyzer != "french_light" {
		return text
	}
	text = frenchLigatures.Replace(strings.ToLower(text))
	folded := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, norm.NFD.String(text))
	words := strings.FieldsFunc(folded, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := words[:0]
	for _, word := range words {
		if !frenchStopwords[word] {
			out = append(out, frenchLightStem(word))
		}
	}
	return strings.Join(out, " ")
}

// Stopwords apply only to the normalized copy, never other Corpora's fields.
var frenchStopwords = func() map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields("a au aux avec ce ces dans de des du elle en et eux il je la le les leur lui ma mais me meme mes moi mon ne nos notre nous on ou par pas pour qu que qui sa se ses son sur ta te tes toi ton tu un une vos votre vous c d j l m n s t y est sont ete etre avait avoir fait") {
		out[w] = true
	}
	return out
}()

func frenchLightStem(word string) string {
	s := []rune(word)
	n := len(s)
	ends := func(suffix string) bool { return strings.HasSuffix(string(s[:n]), suffix) }
	if n > 5 && s[n-1] == 'x' {
		if s[n-3] == 'a' && s[n-2] == 'u' && s[n-4] != 'e' {
			s[n-2] = 'l'
		}
		n--
	}
	if n > 3 && s[n-1] == 'x' {
		n--
	}
	if n > 3 && s[n-1] == 's' {
		n--
	}
	switch {
	case n > 9 && ends("issement"):
		n -= 6
		s[n-1] = 'r'
	case n > 8 && ends("issant"):
		n -= 4
		s[n-1] = 'r'
	case n > 6 && ends("ement"):
		n -= 4
		if n > 3 && ends("ive") {
			n--
			s[n-1] = 'f'
		}
	case n > 11 && ends("ficatrice"):
		n -= 5
		s[n-2], s[n-1] = 'e', 'r'
	case n > 10 && ends("ficateur"):
		n -= 4
		s[n-2], s[n-1] = 'e', 'r'
	case n > 9 && ends("catrice"):
		n -= 3
		s[n-4], s[n-3], s[n-2] = 'q', 'u', 'e'
	case n > 8 && ends("cateur"):
		n -= 2
		s[n-4], s[n-3], s[n-2], s[n-1] = 'q', 'u', 'e', 'r'
	case n > 8 && ends("atrice"):
		n -= 4
		s[n-2], s[n-1] = 'e', 'r'
	case n > 7 && ends("ateur"):
		n -= 3
		s[n-2], s[n-1] = 'e', 'r'
	default:
		if n > 6 && ends("trice") {
			n--
			s[n-3], s[n-2], s[n-1] = 'e', 'u', 'r'
		}
		switch {
		case n > 5 && ends("ieme"):
			n -= 4
		case n > 7 && ends("teuse"):
			n -= 2
			s[n-1] = 'r'
		case n > 6 && ends("teur"):
			n--
			s[n-1] = 'r'
		case n > 5 && ends("euse"):
			n -= 2
		case n > 8 && ends("ere"):
			n--
			s[n-2] = 'e'
		case n > 7 && ends("ive"):
			n--
			s[n-1] = 'f'
		case n > 4 && (ends("folle") || ends("molle")):
			n -= 2
			s[n-1] = 'u'
		case n > 9 && ends("nnelle"):
			n -= 5
		case n > 9 && ends("nnel"):
			n -= 3
		default:
			if n > 4 && ends("ete") {
				n--
				s[n-2] = 'e'
			}
			if n > 8 && ends("ique") {
				n -= 4
			}
			switch {
			case n > 8 && ends("esse"):
				n -= 3
			case n > 7 && ends("inage"):
				n -= 3
			case n > 9 && ends("isation"):
				n -= 7
				if n > 5 && ends("ual") {
					s[n-2] = 'e'
				}
			case n > 9 && ends("isateur"):
				n -= 7
			case n > 8 && (ends("ation") || ends("ition")):
				n -= 5
			}
		}
	}
	s = s[:n]
	if len(s) > 4 {
		out := s[:1]
		for _, r := range s[1:] {
			if r != out[len(out)-1] || !unicode.IsLetter(r) {
				out = append(out, r)
			}
		}
		s = out
	}
	if len(s) > 4 && strings.HasSuffix(string(s), "ie") {
		s = s[:len(s)-2]
	}
	if len(s) > 4 {
		if s[len(s)-1] == 'r' {
			s = s[:len(s)-1]
		}
		if s[len(s)-1] == 'e' {
			s = s[:len(s)-1]
		}
		if s[len(s)-1] == 'e' {
			s = s[:len(s)-1]
		}
		if s[len(s)-1] == s[len(s)-2] && unicode.IsLetter(s[len(s)-1]) {
			s = s[:len(s)-1]
		}
	}
	return string(s)
}
