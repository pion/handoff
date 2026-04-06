/*
  SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
  SPDX-License-Identifier: MIT
*/

(() => {
  const globalObject = globalThis;
  const originalRTCPeerConnection = globalObject.RTCPeerConnection;

  let handoffURL = '/handoff';
  let started = false;
  let controlSessionPromise = null;

  function dispatchHandler(handler, payload) {
    if (typeof handler === 'function') {
      handler(payload);
    }
  }

  function waitForIceGatheringComplete(peerConnection) {
    if (peerConnection.iceGatheringState === 'complete') {
      return Promise.resolve();
    }

    return new Promise(resolve => {
      function handleIceGatheringStateChange() {
        if (peerConnection.iceGatheringState !== 'complete') {
          return;
        }

        peerConnection.removeEventListener(
          'icegatheringstatechange',
          handleIceGatheringStateChange,
        );
        resolve();
      }

      peerConnection.addEventListener(
        'icegatheringstatechange',
        handleIceGatheringStateChange,
      );
    });
  }

  function createDataChannelProxy(label) {
    return {
      label,
      onopen: null,
      onmessage: null,
    };
  }

  async function createControlSession(onClose) {
    const controlPeerConnection = new originalRTCPeerConnection();
    const controlChannel = controlPeerConnection.createDataChannel('handoff-control');
    const peerConnections = new Map();
    const pendingRequests = new Map();
    let requestCounter = 0;
    let closed = false;
    let readySettled = false;
    let resolveReady;
    let rejectReady;

    const readyPromise = new Promise((resolve, reject) => {
      resolveReady = () => {
        if (readySettled) {
          return;
        }

        readySettled = true;
        resolve();
      };
      rejectReady = error => {
        if (readySettled) {
          return;
        }

        readySettled = true;
        reject(error);
      };
    });

    function close(error = new Error('handoff control session closed')) {
      if (closed) {
        return;
      }

      closed = true;
      rejectReady(error);

      for (const { reject } of pendingRequests.values()) {
        reject(error);
      }

      pendingRequests.clear();
      peerConnections.clear();
      onClose();

      try {
        controlChannel.close();
      } catch {}

      try {
        controlPeerConnection.close();
      } catch {}
    }

    controlChannel.onopen = () => {
      resolveReady();
    };
    controlChannel.onmessage = messageEvent => {
      const message = JSON.parse(messageEvent.data);
      if (message.kind === 'response') {
        const pendingRequest = pendingRequests.get(message.requestId);
        if (!pendingRequest) {
          return;
        }

        pendingRequests.delete(message.requestId);
        if (message.error) {
          pendingRequest.reject(new Error(message.error));

          return;
        }

        pendingRequest.resolve(message.result);

        return;
      }

      if (message.kind !== 'event') {
        return;
      }

      const peerConnection = peerConnections.get(message.peerConnectionId);
      if (peerConnection) {
        peerConnection.handleEvent(message);
      }
    };
    controlChannel.onclose = () => {
      close();
    };
    controlPeerConnection.onconnectionstatechange = () => {
      if (
        controlPeerConnection.connectionState === 'closed' ||
        controlPeerConnection.connectionState === 'disconnected' ||
        controlPeerConnection.connectionState === 'failed'
      ) {
        close(
          new Error(
            `handoff control session ${controlPeerConnection.connectionState}`,
          ),
        );
      }
    };

    const offer = await controlPeerConnection.createOffer();
    await controlPeerConnection.setLocalDescription(offer);
    await waitForIceGatheringComplete(controlPeerConnection);

    const response = await globalObject.fetch(handoffURL, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ offer: controlPeerConnection.localDescription }),
    });
    if (!response.ok) {
      close(new Error(`handoff bootstrap failed with status ${response.status}`));

      throw new Error(`handoff bootstrap failed with status ${response.status}`);
    }

    const payload = await response.json();
    await controlPeerConnection.setRemoteDescription(payload.answer);
    await readyPromise;

    return {
      registerPeerConnection(id, peerConnection) {
        peerConnections.set(id, peerConnection);
      },
      async sendRequest(event, data, peerConnectionID) {
        if (closed) {
          throw new Error('handoff control session closed');
        }

        await readyPromise;

        return new Promise((resolve, reject) => {
          const requestId = `${++requestCounter}`;
          pendingRequests.set(requestId, { resolve, reject });

          try {
            controlChannel.send(JSON.stringify({
              kind: 'request',
              requestId,
              peerConnectionId: peerConnectionID,
              event,
              data,
            }));
          } catch (error) {
            pendingRequests.delete(requestId);
            reject(error);
          }
        });
      },
      close,
    };
  }

  function getControlSession() {
    if (!started) {
      return Promise.reject(new Error('handoff is not started'));
    }

    if (!controlSessionPromise) {
      let sessionPromise;
      sessionPromise = createControlSession(() => {
        if (controlSessionPromise === sessionPromise) {
          controlSessionPromise = null;
        }
      }).catch(error => {
        if (controlSessionPromise === sessionPromise) {
          controlSessionPromise = null;
        }

        throw error;
      });
      controlSessionPromise = sessionPromise;
    }

    return controlSessionPromise;
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
          .then(() => getControlSession())
          .then(session => this.idPromise.then(id => session.sendRequest(event, data, id)));

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
            dataChannels.set(handoffEvent.dataChannelId, dataChannel);
            dispatchHandler(this.ondatachannel, { channel: dataChannel });

            return;
          }
          case 'datachannelopen': {
            const dataChannel = dataChannels.get(handoffEvent.dataChannelId);
            if (dataChannel) {
              dispatchHandler(dataChannel.onopen);
            }

            return;
          }
          case 'datachannelmessage': {
            const dataChannel = dataChannels.get(handoffEvent.dataChannelId);
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
        return this.enqueueCall('createOffer', []);
      },
      async setLocalDescription(description) {
        await this.enqueueCall('setLocalDescription', [description]);
      },
      async setRemoteDescription(description) {
        await this.enqueueCall('setRemoteDescription', [description]);
      },
      async createAnswer() {
        return this.enqueueCall('createAnswer', []);
      },
      async addIceCandidate(candidate) {
        await this.enqueueCall('addIceCandidate', [candidate]);
      },
    };

    peerConnection.idPromise = getControlSession()
      .then(session => session.sendRequest('new', args).then(id => {
        session.registerPeerConnection(id, peerConnection);

        return id;
      }));

    return peerConnection;
  }

  function Start(options = {}) {
    if (started) {
      return getControlSession();
    }

    handoffURL = options.handoffURL ?? handoffURL;
    globalObject.RTCPeerConnection = function(...args) {
      return wrapPeerConnection(args);
    };
    started = true;

    return getControlSession();
  }

  function Stop() {
    if (!started) {
      return;
    }

    const sessionPromise = controlSessionPromise;

    started = false;
    controlSessionPromise = null;
    handoffURL = '/handoff';
    globalObject.RTCPeerConnection = originalRTCPeerConnection;

    if (sessionPromise) {
      void sessionPromise.then(session => {
        session.close();
      }).catch(() => {});
    }
  }

  globalObject.handoff = Object.freeze({
    Start,
    Stop,
  });
})();
