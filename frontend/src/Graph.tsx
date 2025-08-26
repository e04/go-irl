import { useEffect, useRef } from "react";
import * as echarts from "echarts";

type DataItem = {
  timepointUnixMs: number;
  bitrate: number;
  rtt: number;
  loss: number;
};

const DURATION = 1000 * 60;

export const Graph = ({
  data,
  isReceiving,
  isDisconnected,
}: {
  data: (DataItem | null)[];
  isReceiving: boolean;
  isDisconnected: boolean;
}) => {
  const chartRef = useRef<HTMLDivElement>(null);
  const nonNullData = data.filter((d) => d != null);
  const lastItem = nonNullData[nonNullData.length - 1];

  useEffect(() => {
    if (!chartRef.current) return;
    const chart = echarts.init(chartRef.current);

    const option = {
      backgroundColor: "rgba(0, 0, 0, 0.9)",
      animation: false,
      tooltip: { show: false },
      legend: {
        show: false,
      },
      grid: {
        left: 0,
        top: 0,
        right: 0,
        bottom: 30,
      },
      xAxis: {
        type: "time",
        axisLabel: { show: false },
        axisLine: { lineStyle: { color: "#757575" } },
        min: Date.now() - DURATION,
        max: Date.now(),
      },
      yAxis: [
        {
          type: "value",
          name: "Bitrate",
          position: "right",
          min: 0,
          max: 10,
          show: false,
        },
        {
          type: "log",
          name: "RTT",
          position: "right",
          min: 20,
          max: 2000,
          show: false,
        },
        {
          type: "value",
          name: "loss",
          position: "left",
          min: 0,
          max: 1,
          show: false,
        },
      ],
      series: [
        {
          name: "dummy",
          type: "scatter",
          data: data.map((d) => [d?.timepointUnixMs ?? 0, 0]),
          symbolSize: 0,
        },
        {
          name: "Bitrate(Mbps)",
          type: "scatter",
          symbolSize: 5,
          yAxisIndex: 0,
          data: nonNullData.map((d) => [d.timepointUnixMs, d.bitrate]),
          itemStyle: {
            color: "#42A5F5",
          },
        },
        {
          name: "RTT(ms)",
          type: "scatter",
          symbolSize: 5,
          yAxisIndex: 1,
          data: nonNullData.map((d) => [
            d.timepointUnixMs,
            d.rtt < 20 ? 20 : d.rtt,
          ]),
          itemStyle: {
            color: "#66BB6A",
          },
        },
        {
          name: "Loss(%)",
          type: "scatter",
          yAxisIndex: 2,
          stack: "bytes",
          data: nonNullData.map((d) => [
            d.timepointUnixMs,
            d.loss === 0 ? -Infinity : d.loss,
          ]),
          itemStyle: {
            color: "#FFB74D",
          },
          symbolSize: 5,
        },
      ],
    };

    chart.setOption(option);
    return () => chart.dispose();
  }, [data]);

  return (
    <div
      style={{
        width: "100vw",
        height: "100vh",
        borderRadius: 5,
        overflow: "hidden",
        position: "relative",
      }}
    >
      <div
        ref={chartRef}
        style={{
          width: "100%",
          height: "100%",
        }}
      />
      <div
        style={{
          position: "absolute",
          bottom: 8,
          left: 16,
          backgroundColor: isDisconnected
            ? "#CFD8DC"
            : (nonNullData[nonNullData.length - 1]?.loss ?? 0) > 0.2
            ? "#E57373"
            : (nonNullData[nonNullData.length - 1]?.loss ?? 0) > 0.05
            ? "#FFC107"
            : "#8BC34A",
          opacity: isReceiving ? 1 : 0.25,
          borderRadius: 12,
          width: 12,
          height: 12,
        }}
      />
      {lastItem != null && (
        <div
          style={{
            display: isDisconnected ? "none" : "flex",
            position: "absolute",
            bottom: 2,
            left: 0,
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
            {(lastItem.loss * 100).toFixed(1)}%
          </div>
        </div>
      )}
    </div>
  );
};
