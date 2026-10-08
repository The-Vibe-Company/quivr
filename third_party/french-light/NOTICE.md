# French light stemming provenance

`internal/keywords/french_light.go` adapts Apache Lucene's FrenchLightStemmer
(UniNE algorithm), from commit `1f4da953f0c13b9b5a3a38dfcfc3c7b6a5a2f6cd`:
[upstream source](https://github.com/apache/lucene/blob/1f4da953f0c13b9b5a3a38dfcfc3c7b6a5a2f6cd/lucene/analysis/common/src/java/org/apache/lucene/analysis/fr/FrenchLightStemmer.java).

The Go adaptation uses Unicode accent folding before stemming and a separate
French stopword list. It is used only for configured keyword copies. Original
content and vector input are preserved.

The Apache-2.0 license is in [LICENSE.apache-2.0](LICENSE.apache-2.0).
The relevant attribution from the pinned [Lucene NOTICE](https://github.com/apache/lucene/blob/1f4da953f0c13b9b5a3a38dfcfc3c7b6a5a2f6cd/NOTICE.txt) is:

```text
Apache Lucene
Copyright 2001-2025 The Apache Software Foundation

This product includes software developed at
The Apache Software Foundation (http://www.apache.org/).
```

Lucene also attributes its light stemmers to the BSD reference implementations
by Jacques Savoy and Ljiljana Dolamic. The French source retains Savoy's notice below.
The upstream source additionally retains the following Savoy copyright and
BSD notice. Distributions of the compiled engine must include both notices.

```text
Copyright (c) 2005, Jacques Savoy
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

Redistributions of source code must retain the above copyright notice, this
list of conditions and the following disclaimer. Redistributions in binary
form must reproduce the above copyright notice, this list of conditions and
the following disclaimer in the documentation and/or other materials
provided with the distribution. Neither the name of the author nor the names
of its contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.
```
