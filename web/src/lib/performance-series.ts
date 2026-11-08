export type PerformanceCategory = {
  id: string;
  code: string;
  name: string;
  total: string;
  values: string[];
};
export type PerformanceSide = {
  total: string;
  values: string[];
  categories: PerformanceCategory[];
};
export type PerformanceData = {
  period: { from: string; to: string };
  grain: "day" | "week" | "month" | "quarter" | "year";
  /** Present when the route could not place amounts in calendar periods, with its reason. */
  unavailable?: string;
  buckets: { from: string; to: string }[];
  income: PerformanceSide;
  expenses: PerformanceSide;
  domain: { min: string; max: string };
};
