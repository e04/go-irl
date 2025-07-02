# go-irl: A modern SRTLA Streaming Stack


![Screenshot 2025-07-02 23-19-20](https://github.com/user-attachments/assets/cee3e079-aea7-4f95-8069-2c9e020840ca)


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

<img width="1671" src="https://github.com/user-attachments/assets/39afdd63-dfc6-4bcb-b066-f8510dfc055d" />

## Getting Started

Follow these steps to download the tools, run the stack, and configure OBS.

### Prerequisites

- **OBS Studio Installed**: You must have a recent version of [OBS Studio](https://obsproject.com/).
- **Publicly Accessible Port**: Your PC must be accessible from the internet on the port you choose for `go-srtla` (the default is port **5000**). This usually requires **port forwarding** on your home router to direct incoming traffic on TCP/UDP port 5000 to your PC's local IP address.
- **Custom Config**: If you need to use a different port or set an SRT passphrase, open the launcher script (`go-irl.sh` / `_go-irl.ps1`) and adjust the command-line flags accordingly.

---

### Part 1: Download and Run the Stack

1.  **Get the Launcher and Binaries (choose one)**:

    **Option A — Download the all-in-one _go-irl_ bundle**  
    Pre-built bundles for Windows, Linux, and macOS are published on this repository's
    [Releases](https://github.com/e04/go-irl/releases) page. Download the archive that matches your operating system / architecture:

    1. Extract the archive to any folder you like.
    2. The extracted directory already contains **all three component binaries** and the launcher script(s) for your platform, so you can proceed directly to the next step.

    **Option B — Download each component manually**  
    If you prefer to assemble the stack yourself (or want to use custom builds), **clone or download this repository first** to obtain the launcher scripts, then download the latest binaries for each component and place them in the same folder:

    - [**go-srtla Releases**](https://github.com/e04/go-srtla/releases)
    - [**srt-live-reporter Releases**](https://github.com/e04/srt-live-reporter/releases)
    - [**obs-srt-bridge Releases**](https://github.com/e04/obs-srt-bridge/releases)

    After placing the executables, your directory should resemble the example shown above (with platform-specific file extensions).

2.  **(Linux/macOS) Make the Script Executable**:
    Windows users can skip this step. Open your terminal, navigate to the project directory, and run:

    ```bash
    chmod +x go-irl.sh
    ```

3.  **Run the Stack**:
    Execute the appropriate launcher to start all three services in a single terminal window.

    - **Linux/macOS**:

      ```bash
      ./go-irl.sh
      ```

    - **Windows**:
      Simply double-click the `go-irl-windows.bat` file,

      or run it from Command Prompt:
      ```cmd
      go-irl-windows.bat
      ```

---

### Part 2: Configure OBS Studio

Now, configure OBS to receive the stream and use the bridge for stats and scene switching.

1.  **Create Scenes:**

    - Create two scenes in OBS. For this guide, we'll name them **`ONLINE`** and **`OFFLINE`**. The `OFFLINE` scene can contain a "Be Right Back" message, an image, or a video loop.
  

<img width="500" src="https://github.com/user-attachments/assets/d90b5a8f-1f70-4b68-a5c8-df5397d29bf9" />

2.  **Add the Media Source (Video Feed):**

    - Go to the **`ONLINE`** scene.
    - Add a new source by clicking the `+` button in the "Sources" dock and select **Media Source**.
    - Give it a name (e.g., "SRT Feed").
    - **Uncheck** the box for "Local File".
    - In the "Input" field, enter `udp://127.0.0.1:5002`.
    - In the "Input Format" field, enter `mpegts`.
    - **IMPORTANT:** **Uncheck** the box for `Restart playback when source becomes active`. This prevents the video from stuttering every time `obs-srt-bridge` switches back to this scene.
    - Click OK.
  
<img width="800" src="https://github.com/user-attachments/assets/cecc8460-1fa0-420f-8310-9307849e9703" />

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

<img width="800" src="https://github.com/user-attachments/assets/6bb9e601-a2e1-453c-98e0-ea6488f838e4" />

---

### Part 3: Configure Your Mobile App

Finally, configure your mobile streaming app (e.g., IRL Pro, Moblin, or BELABOX).

1.  Set the destination URL to point to your PC's **public IP address** and the port you configured.

    ```
    srtla://<YOUR_PUBLIC_IP_ADDRESS>:5000
    ```

    - Replace `<YOUR_PUBLIC_IP_ADDRESS>` with your actual public IP. You can find this by searching "what is my IP" in a browser on your PC.
    - The port `5000` is the default port listened on by `go-srtla` via the launcher script.

You are now ready to start streaming!
