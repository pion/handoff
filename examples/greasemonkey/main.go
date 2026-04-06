// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

//go:build !js

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"github.com/pion/handoff"
)

func main() {
	origin := flag.String("origin", "http://localhost:8080", "handoff server origin")
	flag.Parse()

	pageScript, err := json.Marshal(`
(() => {
  const requestType = "__handoff_bootstrap_request__";
  const responseType = "__handoff_bootstrap_response__";
  const pending = new Map();
  let requestID = 0;

  window.__handoffBootstrap = offer => new Promise((resolve, reject) => {
    const id = String(++requestID);
    pending.set(id, { resolve, reject });
    window.postMessage({
      type: requestType,
      id,
      offer: { type: offer.type, sdp: offer.sdp },
    }, "*");
  });

  window.addEventListener("message", event => {
    const data = event.data;
    if (!data || data.type !== responseType) {
      return;
    }

    const request = pending.get(data.id);
    if (!request) {
      return;
    }

    pending.delete(data.id);
    if (data.error) {
      request.reject(new Error(data.error));
      return;
    }

    request.resolve(data.answer);
  });
})();
` + handoff.JavaScript() + `
handoff.Start({ bootstrap: window.__handoffBootstrap });
`)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf(`// ==UserScript==
// @name         Pion Handoff
// @namespace    https://github.com/pion/handoff
// @version      0.0.1
// @match        *://*/*
// @grant        GM_xmlhttpRequest
// @connect      *
// @run-at       document-start
// ==/UserScript==

(() => {
  const handoffOrigin = %q;
  const requestType = "__handoff_bootstrap_request__";
  const responseType = "__handoff_bootstrap_response__";
  const pageScript = %s;

  window.addEventListener("message", event => {
    const data = event.data;
    if (!data || data.type !== requestType) {
      return;
    }

    GM_xmlhttpRequest({
      method: "POST",
      url: handoffOrigin + "/handoff",
      headers: { "Content-Type": "application/json" },
      data: JSON.stringify({ offer: data.offer }),
      onload: response => {
        if (response.status < 200 || response.status >= 300) {
          window.postMessage({ type: responseType, id: data.id, error: "handoff bootstrap failed" }, "*");
          return;
        }

        try {
          const payload = JSON.parse(response.responseText);
          window.postMessage({ type: responseType, id: data.id, answer: payload.answer }, "*");
        } catch (error) {
          window.postMessage({ type: responseType, id: data.id, error: error.message }, "*");
        }
      },
      onerror: response => {
        window.postMessage({
          type: responseType,
          id: data.id,
          error: response.error || "handoff bootstrap failed",
        }, "*");
      },
    });
  });

  const script = document.createElement("script");
  script.textContent = pageScript;
  (document.head || document.documentElement).appendChild(script);
  script.remove();
})();
`, *origin, string(pageScript))
}
