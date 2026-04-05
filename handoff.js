/*
  SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
  SPDX-License-Identifier: MIT
*/

(() => {
  const globalObject = globalThis;
  const originalRTCPeerConnection = globalObject.RTCPeerConnection;

  let handoffURL = '/handoff';
  let eventsURL = '/handoff/events';
  let started = false;

  function delay(milliseconds) {
    return new Promise(resolve => globalObject.setTimeout(resolve, milliseconds));
  }

  function dispatchHandler(handler, payload) {
    if (typeof handler === 'function') {
      handler(payload);
    }
  }

  function parseJSONText(text) {
    if (!text) {
      return null;
    }

    return JSON.parse(text);
  }

  async function postEvent(event, data, id) {
    const payload = { event, data };
    if (id) {
      payload.id = id;
    }

    const response = await globalObject.fetch(handoffURL, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });

    if (!response.ok) {
      throw new Error(`handoff request failed with status ${response.status}`);
    }

    if (response.status === 204) {
      return null;
    }

    return response.text();
  }

  async function fetchEvents(id) {
    const response = await globalObject.fetch(
      `${eventsURL}?id=${encodeURIComponent(id)}`,
    );
    if (!response.ok) {
      throw new Error(`handoff event poll failed with status ${response.status}`);
    }

    if (response.status === 204) {
      return [];
    }

    const body = await response.text();
    if (!body) {
      return [];
    }

    return body
      .split('\n')
      .filter(line => line.length > 0)
      .map(line => JSON.parse(line));
  }

  function createDataChannelProxy(label) {
    return {
      label,
      onopen: null,
      onmessage: null,
    };
  }

  function startEventPolling(peerConnection) {
    void peerConnection.idPromise.then(async id => {
      for (;;) {
        try {
          const events = await fetchEvents(id);
          for (const handoffEvent of events) {
            peerConnection.handleEvent(handoffEvent);
          }
        } catch {
          await delay(1000);
        }

        await delay(250);
      }
    }).catch(() => {});
  }

  function wrapPeerConnection(args) {
    const dataChannels = new Map();
    const peerConnection = {
      connectionState: 'new',
      onconnectionstatechange: null,
      ondatachannel: null,
      onicecandidate: null,
      operationQueue: Promise.resolve(),
      enqueueCall(event, data) {
        const operation = this.operationQueue
          .then(() => this.idPromise)
          .then(id => postEvent(event, data, id));

        this.operationQueue = operation.then(() => null, () => null);

        return operation;
      },
      handleEvent(handoffEvent) {
        switch (handoffEvent.name) {
          case 'connectionstatechange':
            this.connectionState = handoffEvent.data.connectionState;
            dispatchHandler(this.onconnectionstatechange);

            return;
          case 'icecandidate':
            dispatchHandler(this.onicecandidate, { candidate: handoffEvent.data });

            return;
          case 'datachannel': {
            const dataChannel = createDataChannelProxy(handoffEvent.data.label);
            dataChannels.set(handoffEvent.dataChannelID, dataChannel);
            dispatchHandler(this.ondatachannel, { channel: dataChannel });

            return;
          }
          case 'datachannelopen': {
            const dataChannel = dataChannels.get(handoffEvent.dataChannelID);
            if (dataChannel) {
              dispatchHandler(dataChannel.onopen);
            }

            return;
          }
          case 'datachannelmessage': {
            const dataChannel = dataChannels.get(handoffEvent.dataChannelID);
            if (dataChannel) {
              dispatchHandler(dataChannel.onmessage, { data: handoffEvent.data.data });
            }
          }
        }
      },
      createDataChannel(label, options) {
        let dataChannel;
        const dataChannelID = this.enqueueCall('createDataChannel', [label, options ?? null])
          .then(responseID => {
            if (responseID) {
              dataChannels.set(responseID, dataChannel);
            }

            return responseID || '';
          });

        dataChannel = createDataChannelProxy(label);

        return dataChannel;
      },
      async createOffer() {
        return parseJSONText(await this.enqueueCall('createOffer', []));
      },
      async setLocalDescription(description) {
        await this.enqueueCall('setLocalDescription', [description]);
      },
      async setRemoteDescription(description) {
        await this.enqueueCall('setRemoteDescription', [description]);
      },
      async createAnswer() {
        return parseJSONText(await this.enqueueCall('createAnswer', []));
      },
      async addIceCandidate(candidate) {
        await this.enqueueCall('addIceCandidate', [candidate]);
      },
    };

    peerConnection.idPromise = postEvent('new', args).then(id => {
      if (!id) {
        throw new Error('missing backend peer connection id');
      }

      startEventPolling(peerConnection);

      return id;
    });

    return peerConnection;
  }

  function Start(options = {}) {
    if (started) {
      return;
    }

    handoffURL = options.handoffURL ?? handoffURL;
    eventsURL = options.eventsURL ?? eventsURL;
    globalObject.RTCPeerConnection = function(...args) {
      return wrapPeerConnection(args);
    };
    started = true;
  }

  function Stop() {
    if (!started) {
      return;
    }

    globalObject.RTCPeerConnection = originalRTCPeerConnection;
    handoffURL = '/handoff';
    eventsURL = '/handoff/events';
    started = false;
  }

  globalObject.handoff = Object.freeze({
    Start,
    Stop,
  });
})();
