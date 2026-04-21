import type { Config } from "tailwindcss";

const config: Config = {
  content: [
    "./app/**/*.{ts,tsx}",
    "./components/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        ink: "#131516",
        fog: "#f3efe5",
        rust: "#9b4d2f",
        gold: "#d79b38",
      },
    },
  },
  plugins: [],
};

export default config;
