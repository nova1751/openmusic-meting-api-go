# Attribution

The Go implementation was based on the API contract and selected NetEase
request formats in OpenMusic-Meting-API at
[fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5](https://github.com/qq01-hub/OpenMusic-Meting-API/tree/fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5).
The upstream package identifies xizeyoupan as the package author and declares MIT.

The optional compatibility image downloads the original repository at that
revision. Its Folia-derived QRC implementation retains its own AGPL-3.0
license and attribution. See the upstream
[Folia notice](https://github.com/qq01-hub/OpenMusic-Meting-API/blob/fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5/src/vendor/folia/NOTICE.md)
and [license](https://github.com/qq01-hub/OpenMusic-Meting-API/blob/fb1d08dcf81ea1f63a75fb344f7a60b49492d9f5/src/vendor/folia/LICENSE).

## go-qrcode

The native administrator uses github.com/skip2/go-qrcode:

Copyright (c) 2014 Tom Harwood

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.

## Go DES tables for QRC

The QQ lyric decoder uses DES tables from the Go standard library. The
corresponding BSD license is in [internal/tencent/qrc/LICENSE](./internal/tencent/qrc/LICENSE).
