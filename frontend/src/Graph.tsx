import { useEffect, useRef } from "react";
import * as echarts from "echarts";
import type { StatsSample } from "./stats";

const DURATION = 1000 * 60;
// Stats arrive about once per second, each covering the time since the last.
const SAMPLE_INTERVAL = 1000;
// While stats arrive, the axis ends at the latest one so the newest bar sits at
// the right edge. Once they are this late, it scrolls on with the clock.
const STALE_AFTER = 1500;
// Scroll the time axis even when no stats arrive, e.g. while disconnected.
const AXIS_UPDATE_INTERVAL = 500;

// Where a sample is drawn: the middle of the interval it covers.
function sampleX(sample: StatsSample) {
  return sample.receivedAtMs - SAMPLE_INTERVAL / 2;
}

function xAxisRange(now: number, lastReceivedAtMs: number | undefined) {
  const max =
    lastReceivedAtMs == null
      ? now
      : Math.max(lastReceivedAtMs, now - STALE_AFTER);
  return { min: max - DURATION, max };
}

// Bitrate that cannot be split by link, e.g. right after connecting.
const BITRATE_COLOR = "#546E7A";
// Shades of blue, alternating dark and light so neighboring links in a stack
// stay apart. Links are colored by ID so a link keeps its color while others
// come and go.
const LINK_COLORS = [
  "#0D3C7A",
  "#2E6DB4",
  "#0A2E5C",
  "#4A7FBF",
  "#15498F",
  "#3A74A8",
];

function linkColor(id: number) {
  return LINK_COLORS[id % LINK_COLORS.length];
}

const baseOption: echarts.EChartsOption = {
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
  // A value axis of Unix milliseconds rather than a time axis: with bars on a
  // time axis, ECharts widens the extent by the bar overflow even when min and
  // max are fixed, which pushes "now" left of the right edge.
  xAxis: {
    type: "value",
    splitLine: { show: false },
    axisLabel: { show: false },
    axisLine: { lineStyle: { color: "#757575" } },
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
      name: "Retrans",
      position: "left",
      min: 0,
      max: 100,
      show: false,
    },
  ],
};

function bitrateBar(
  id: string,
  color: string,
  data: [number, number][]
): echarts.BarSeriesOption {
  return {
    id,
    type: "bar",
    stack: "bitrate",
    yAxisIndex: 0,
    // ECharts stacks the first numeric column, which on a value axis would be
    // the time, so the data is [Mbps, time].
    encode: { x: 1, y: 0 },
    // Neighboring bars touch. The slot is the smallest gap between samples, and
    // stats arrive with some jitter, so bars are made a little wider than it
    // to leave no seams where samples are further apart.
    barWidth: "120%",
    itemStyle: { color },
    data,
  };
}

// One bar per sample for the bitrate, split into each SRTLA link's share.
// Every series has a value for every sample so the stacks line up.
function bitrateSeries(data: StatsSample[]): echarts.BarSeriesOption[] {
  const linkIds = [
    ...new Set(data.flatMap((d) => d.links.map((link) => link.id))),
  ].sort((a, b) => a - b);
  const series = linkIds.map((id) =>
    bitrateBar(
      `link-${id}`,
      linkColor(id),
      data.map((d) => [
        d.bitrate * (d.links.find((link) => link.id === id)?.share ?? 0),
        sampleX(d),
      ])
    )
  );
  // Samples without link shares, e.g. the first after connecting or a
  // publisher that does not go through SRTLA.
  if (data.some((d) => d.links.length === 0)) {
    series.unshift(
      bitrateBar(
        "bitrate",
        BITRATE_COLOR,
        data.map((d) => [d.links.length === 0 ? d.bitrate : 0, sampleX(d)])
      )
    );
  }
  return series;
}

export const Graph = ({
  data,
  isDisconnected,
}: {
  data: StatsSample[];
  isDisconnected: boolean;
}) => {
  const chartRef = useRef<HTMLDivElement>(null);
  const chartInstance = useRef<echarts.ECharts | null>(null);
  const lastItem = data[data.length - 1];

  // Create the chart once; re-initializing on every update is expensive in
  // the OBS browser source.
  useEffect(() => {
    if (!chartRef.current) return;
    const chart = echarts.init(chartRef.current);
    chart.setOption(baseOption);
    chartInstance.current = chart;

    const handleResize = () => chart.resize();
    window.addEventListener("resize", handleResize);

    return () => {
      window.removeEventListener("resize", handleResize);
      chart.dispose();
      chartInstance.current = null;
    };
  }, []);

  // Samples carry the client's reception time, so the axis uses the same clock.
  const lastReceivedAtMs = lastItem?.receivedAtMs;
  const lastReceivedAtRef = useRef(lastReceivedAtMs);
  lastReceivedAtRef.current = lastReceivedAtMs;

  useEffect(() => {
    const intervalId = setInterval(() => {
      chartInstance.current?.setOption({
        xAxis: xAxisRange(Date.now(), lastReceivedAtRef.current),
      });
    }, AXIS_UPDATE_INTERVAL);
    return () => clearInterval(intervalId);
  }, []);

  useEffect(() => {
    // Links come and go, so the series are replaced rather than merged. The
    // axis moves in the same update so a new bar never lands past its end.
    chartInstance.current?.setOption(
      {
        xAxis: xAxisRange(Date.now(), lastReceivedAtMs),
        series: [
          ...bitrateSeries(data),
          {
            id: "rtt",
            name: "RTT(ms)",
            type: "scatter",
            symbolSize: 5,
            yAxisIndex: 1,
            itemStyle: {
              color: "#66BB6A",
            },
            data: data.map((d) => [sampleX(d), d.rtt < 20 ? 20 : d.rtt]),
          },
          {
            id: "retrans",
            name: "Retrans(%)",
            type: "scatter",
            symbolSize: 5,
            yAxisIndex: 2,
            itemStyle: {
              color: "#FFB74D",
            },
            data: data.map((d) => [
              sampleX(d),
              d.retransRate ? d.retransRate : -Infinity,
            ]),
          },
        ],
      },
      { replaceMerge: ["series"] }
    );
  }, [data, lastReceivedAtMs]);

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
            : (lastItem?.retransRate ?? 0) > 20
            ? "#E57373"
            : (lastItem?.retransRate ?? 0) > 5
            ? "#FFC107"
            : "#8BC34A",
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
            {lastItem.retransRate?.toFixed(1) ?? "-"}%
          </div>
        </div>
      )}
    </div>
  );
};
