import { Graph } from "./Graph";
import { SimpleText } from "./SimpleText";
import { useWebSocket } from "./useWebSocket";

function App() {
  const urlParams = new URLSearchParams(window.location.search);
  const displayType = urlParams.get("type") || "simple";
  const wsPort = urlParams.get("wsport") || "8888";

  const ENDPOINT = `ws://localhost:${wsPort}/ws`;

  const onlineSceneName = urlParams.get("onlineSceneName") || "ONLINE";
  const offlineSceneName = urlParams.get("offlineSceneName") || "OFFLINE";

  const { messages, isReceiving, isDisconnected } = useWebSocket({
    url: ENDPOINT,
    onConnected: () => {
      console.log("connected");
      window.obsstudio?.setCurrentScene(onlineSceneName);
    },
    onDisconnected: () => {
      console.log("disconnected");
      window.obsstudio?.setCurrentScene(offlineSceneName);
    },
    onGoodConnection: () => {
      console.log("good connection");
      window.obsstudio?.setCurrentScene(onlineSceneName);
    },
    onPoorConnection: () => {
      console.log("poor connection");
      window.obsstudio?.setCurrentScene(offlineSceneName);
    },
  });

  const data = messages.map((item) => {
    if (!item) {
      return null;
    }
    return {
      timepointUnixMs: new Date(item.timestamp).getTime(),
      bitrate: item.stats.Instantaneous.MbpsRecvRate,
      rtt: item.stats.Instantaneous.MsRTT,
      loss: item.stats.Instantaneous.PktRecvLossRate / 100,
    };
  });

  const renderComponent = () => {
    switch (displayType) {
      case "simple":
        return (
          <SimpleText
            data={data}
            isReceiving={isReceiving}
            isDisconnected={isDisconnected}
          />
        );
      case "graph":
        return (
          <Graph
            data={data}
            isReceiving={isReceiving}
            isDisconnected={isDisconnected}
          />
        );
      case "none":
        return null;
    }
  };

  return renderComponent();
}

export default App;
