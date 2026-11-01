/** @type {import('tailwindcss').Config} */
export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{js,jsx}"],
  theme: {
    extend: {
      fontFamily: {
        // System serifs only (no webfont download, CSP-friendly): reads like
        // a printed note rather than a SaaS dashboard.
        display: ['"Iowan Old Style"', '"Palatino Linotype"', "Palatino", "Charter", "Georgia", "serif"],
      },
      colors: {
        // Warm "paper & ink" neutrals replace Tailwind's cold blue-grey slate,
        // so every existing slate-* class picks up the new character.
        slate: {
          50: "#f6f2ea",
          100: "#ece6da",
          200: "#ddd5c6",
          300: "#c4baa7",
          400: "#9b917f",
          500: "#786f5f",
          600: "#5a5246",
          700: "#413b32",
          800: "#2b2722",
          900: "#1d1a16",
          950: "#141210",
        },
        white: "#fffdf8", // card stock, not hospital white
        // Deep ink-green: calm, a bit botanical, nothing like the usual
        // indigo/violet/amber product accents.
        brand: {
          50: "#ecf5f1",
          100: "#d3e9e1",
          200: "#a8d3c5",
          400: "#4fa38e",
          500: "#2f8a73",
          600: "#1f6f5c",
          700: "#17594a",
        },
        marker: "#f4e285", // highlighter yellow
      },
      keyframes: {
        "toast-in": {
          "0%": { opacity: 0, transform: "translateY(6px)" },
          "100%": { opacity: 1, transform: "translateY(0)" },
        },
        spark: {
          "0%, 100%": { opacity: 0.9, transform: "scale(1)" },
          "50%": { opacity: 0.5, transform: "scale(0.7)" },
        },
      },
      animation: { spark: "spark 0.9s ease-in-out infinite" },
    },
  },
  plugins: [],
}
