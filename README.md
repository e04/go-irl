# go-irl: A modern SRTLA Streaming Stack

`go-irl` is a complete, open-source streaming stack designed for creating robust and resilient IRL (In Real Life) broadcasts. It acts as a self-hosted receiver for popular SRTLA bonding clients, allowing you to achieve professional-quality streams over unstable network conditions.

**This stack is fully compatible with mobile and hardware clients like [IRL Pro](https://irlpro.app/) (Android), [Moblin](https://github.com/eerimoq/moblin/) (iOS), and [BELABOX](https://belabox.net/).**

## Key Features

- **Free and Open Source**: This entire stack is free and open-source, giving you full control and transparency over your streaming infrastructure without licensing fees.
- **Cross-Platform & Docker-Free**: Runs natively on Windows, macOS, and Linux with pre-built binaries. No complex Docker setups or virtualization needed—just download the executables and run.
- **Network Bonding**: Combine multiple internet connections to increase bandwidth and reliability, minimizing the impact of packet loss from any single connection.
- **Intelligent Automatic Scene Switching**: `go-irl` uses detailed SRT statistics like **packet loss** to make smarter switching decisions. It automatically switches to a predefined "offline" scene when network quality degrades and seamlessly returns to your main scene once the connection stabilizes.
- **Real-time Health Monitoring**: Get a clear, visual overview of your stream's performance with live statistics displayed directly in OBS.
- **One-Command Setup**: Use the provided launcher scripts (`go-irl.sh`, `go-irl-windows.bat`) to launch, monitor, and manage the entire stack with a single command on Linux, macOS, and Windows.

## Core Components

The `go-irl` stack is built upon three key components that work in tandem:

1.  **[go-srtla](https://github.com/e04/go-srtla)**  
    An SRTLA (SRT Link Aggregation) receiver. It acts as the primary ingest point, receiving streams from multiple network paths and bonding them into a single, stable SRT stream.

2.  **[srt-live-reporter](https://github.com/e04/srt-live-reporter)**  
    A specialized SRT proxy that sits downstream from `go-srtla`. It relays the aggregated stream while exposing detailed, real-time connection statistics (bitrate, RTT, packet loss) via a WebSocket server.

3.  **[obs-srt-bridge](https://github.com/e04/obs-srt-bridge)**  
    The final link in the chain. This tool provides a web-based OBS Browser Source that connects to `srt-live-reporter`'s WebSocket. It displays live statistics inside OBS and can automatically switch scenes based on stream health.

### How It Works

The data flows through the components to create a resilient and automated workflow. The launcher script manages all three components automatically.

<img width="1671" alt="3app" src="https://github.com/user-attachments/assets/39afdd63-dfc6-4bcb-b066-f8510dfc055d" />

## Getting Started

Follow these steps to download the tools, run the stack, and configure OBS.

### Prerequisites

- **OBS Studio Installed**: You must have a recent version of [OBS Studio](https://obsproject.com/).
- **Publicly Accessible Port**: Your PC must be accessible from the internet on the port you choose for `go-srtla` (the default is port **5000**). This usually requires **port forwarding** on your home router to direct incoming traffic on TCP/UDP port 5000 to your PC's local IP address.

---

### Part 1: Download and Run the Stack

1.  **Clone This Repository**

    Choose one of the following methods:

    - **Using Git Command**:

      ```bash
      git clone https://github.com/e04/go-irl.git
      cd go-irl
      ```

    - **Using ZIP Download**:
      1.  Visit the [go-irl repository](https://github.com/e04/go-irl)
      2.  Click the green "Code" button and select "Download ZIP".
      3.  Extract the downloaded ZIP file to your desired location.

2.  **Download the Binaries**:
    Download the latest pre-built binaries for each component from the links below and place them in your cloned `go-irl` repository directory (the same directory as the launcher script):

    - [**go-srtla Releases**](https://github.com/e04/go-srtla/releases)
    - [**srt-live-reporter Releases**](https://github.com/e04/srt-live-reporter/releases)
    - [**obs-srt-bridge Releases**](https://github.com/e04/obs-srt-bridge/releases)

    Your directory should look like this:

    ```
    .
    ├── README.md
    ├── go-srtla
    ├── srt-live-reporter
    ├── obs-srt-bridge
    ├── go-irl.sh         # Linux/macOS
    ├── go-irl-windows.bat  # Windows
    └── _go-irl.ps1       # Windows Helper
    ```

3.  **(Linux/macOS) Make the Script Executable**:
    Windows users can skip this step. Open your terminal, navigate to the project directory, and run:

    ```bash
    chmod +x go-irl.sh
    ```

4.  **Run the Stack**:
    Execute the appropriate launcher to start all three services in a single terminal window.

    - **Linux/macOS**:

      ```bash
      ./go-irl.sh
      ```

    - **Windows**:
      Simply double-click the `go-irl-windows.bat` file, or run it from Command Prompt:
      ```cmd
      go-irl-windows.bat
      ```

    Leave this terminal window running. It manages all the components.

---

### Part 2: Configure OBS Studio

Now, configure OBS to receive the stream and use the bridge for stats and scene switching.

1.  **Create Scenes:**

    - Create two scenes in OBS. For this guide, we'll name them **`ONLINE`** and **`OFFLINE`**. The `OFFLINE` scene can contain a "Be Right Back" message, an image, or a video loop.

2.  **Add the Media Source (Video Feed):**

    - Go to the **`ONLINE`** scene.
    - Add a new source by clicking the `+` button in the "Sources" dock and select **Media Source**.
    - Give it a name (e.g., "SRT Feed").
    - **Uncheck** the box for "Local File".
    - In the "Input" field, enter `udp://127.0.0.1:5002`.
    - In the "Input Format" field, enter `mpegts`.
    - **IMPORTANT:** **Uncheck** the box for `Restart playback when source becomes active`. This prevents the video from stuttering every time `obs-srt-bridge` switches back to this scene.
    - Click OK.

3.  **Add the Browser Source (Stats and Scene Switching):**

    - In the **`ONLINE`** scene, add a new source by clicking `+` and selecting **Browser**.
    - Give it a name (e.g., "SRT Stats").
    - In the "URL" field, enter the following URL. You can customize the parameters as needed.

      ```
      http://localhost:9999/app?wsport=8888&onlineSceneName=ONLINE&offlineSceneName=OFFLINE&type=simple
      ```

      - `wsport=8888`: Tells the bridge to connect to the WebSocket on port **8888** (from `srt-live-reporter`).
      - `onlineSceneName=ONLINE`: The name of your "good connection" scene.
      - `offlineSceneName=OFFLINE`: The name of your "bad connection" scene.
      - `type=simple`: The display type for stats. Can be `simple`, `graph`, or `none`.

    - Set the Width and Height as desired.
    - **IMPORTANT:** For automatic scene switching to work, scroll down in the properties window and set **Page permissions** to **Advanced access to OBS**.
    - Click OK.

---

### Part 3: Configure Your Mobile App

Finally, configure your mobile streaming app (e.g., IRL Pro, Moblin, or BELABOX).

1.  Set the destination URL to point to your PC's **public IP address** and the port you configured for `go-srtla`.

    ```
    srtla://<YOUR_PUBLIC_IP_ADDRESS>:5000
    ```

    - Replace `<YOUR_PUBLIC_IP_ADDRESS>` with your actual public IP. You can find this by searching "what is my IP" in a browser on your PC.
    - The port `5000` is the default port listened on by `go-srtla` via the launcher script.

You are now ready to start streaming!

### How the Launcher Script Works (Default Ports)

The `go-irl` launcher script simplifies setup by running all components with pre-configured default settings. Here is how the data flows:

1.  **`go-srtla`**: Listens for incoming SRTLA connections on port **5000**. It aggregates the streams and forwards a single SRT stream to `127.0.0.1:5001`.
2.  **`srt-live-reporter`**: Listens for the SRT stream from `go-srtla` on port **5001**. It then forwards the video data to OBS via UDP on port **5002** and starts a WebSocket server on port **8888** for statistics.
3.  **`obs-srt-bridge`**: Starts a web server on port **9999**, which serves the OBS Browser Source you add to your scene.

> ** NOTE:** If you want to use different port numbers or add an SRT encryption passphrase, edit the launcher scripts (`go-irl.sh` on Linux/macOS, `_go-irl.ps1` on Windows). These scripts are where the ports and passphrase passed to each component are defined and can be customised.
