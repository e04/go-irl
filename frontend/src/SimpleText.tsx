import type { StatsSample } from "./stats";

interface SimpleTextProps {
  data: StatsSample[];
  isDisconnected: boolean;
}

export function SimpleText({ data, isDisconnected }: SimpleTextProps) {
  const lastItem = data[data.length - 1];

  return (
    <div
      style={{
        width: "400px",
        height: "28px",
        borderRadius: 5,
        overflow: "hidden",
        backgroundColor: "rgba(20, 20, 20, 0.8)",
        display: "flex",
        lineHeight: "28px",
        alignItems: "center",
      }}
    >
      <div
        style={{
          backgroundColor: isDisconnected
            ? "#CFD8DC"
            : (lastItem?.retransRate ?? 0) > 20
            ? "#E57373"
            : (lastItem?.retransRate ?? 0) > 5
            ? "#FFC107"
            : "#8BC34A",
          borderRadius: 12,
          width: 12,
          height: 12,
          margin: "0 8px",
        }}
      />
      {lastItem != null && (
        <div
          style={{
            display: isDisconnected ? "none" : "flex",
            fontFamily: "monospace",
            fontSize: 20,
            color: "#CFD8DC",
            gap: 8,
            justifyContent: "space-between",
            width: "100%",
            padding: "0 12px",
            boxSizing: "border-box",
          }}
        >
          <div
            style={{
              textAlign: "right",
              width: 120,
              whiteSpace: "pre",
              color: "#42A5F5",
            }}
          >
            {lastItem.bitrate.toFixed(1)}
            Mbps
          </div>
          <div
            style={{
              textAlign: "right",
              width: 100,
              whiteSpace: "pre",
              color: "#66BB6A",
            }}
          >
            {lastItem.rtt.toFixed(0)}
            ms
          </div>
          <div
            style={{
              textAlign: "right",
              width: 100,
              whiteSpace: "pre",
              color: "#FFB74D",
            }}
          >
            {lastItem.retransRate?.toFixed(1) ?? "-"}%
          </div>
        </div>
      )}
    </div>
  );
}
