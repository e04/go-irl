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

  const { samples: data, isDisconnected } = useWebSocket({
    url: ENDPOINT,
    onConnected: () => {
      // The online scene is restored by onGoodConnection once enough stats
      // confirm the link is healthy, not merely on reconnect.
      console.log("connected");
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

  const renderComponent = () => {
    switch (displayType) {
      case "simple":
        return (
          <SimpleText
            data={data}
            isDisconnected={isDisconnected}
          />
        );
      case "graph":
        return (
          <Graph
            data={data}
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
