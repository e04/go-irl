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

The data flows through the components to create a resilient and automated workflow:

## Getting Started

The easiest way to run the `go-irl` stack is by using the included manager script.

### Quick Start Guide

1.  **Download the Binaries**:
    Download the latest pre-built binaries for each component and place them in the same directory as the launcher script (`go-irl.sh`, `go-irl-windows.bat`).

    - [**go-srtla Releases**](https://github.com/e04/go-srtla/releases)
    - [**srt-live-reporter Releases**](https://github.com/e04/srt-live-reporter/releases)
    - [**obs-srt-bridge Releases**](https://github.com/e04/obs-srt-bridge/releases)

    Your directory should look like this:

    ```
    .
    ├── go-srtla
    ├── srt-live-reporter
    ├── obs-srt-bridge
    ├── go-irl.sh   # Linux/macOS
    ├── go-irl-windows.bat  # Windows
    └── _go-irl.ps1 # Windows
    ```

2.  **(Linux/macOS) Make the Script Executable**:
    Windows users can skip this step.

    Open your terminal, navigate to the project directory, and run:

    ```bash
    chmod +x go-irl.sh
    ```

3.  **Run the Stack**:
    Execute the appropriate launcher to start all services:

    - **Linux/macOS**:

      ```bash
      ./go-irl.sh
      ```

    - **Windows (CMD)**:

      ```cmd
      go-irl.bat
      ```
