/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // The public site and the CRM share Savvy's ink, terracotta and sage,
        // but the operational product uses quieter, neutral surfaces. A CRM
        // is read for hours at a time; the warmer editorial treatment belongs
        // in marketing and authentication, not behind every table and form.
        white: "#ffffff",
        paper: {
          DEFAULT: "#f7f8f6",
          bright: "#ffffff",
        },
        ink: {
          DEFAULT: "#1e231f",
          soft: "#323a34",
        },
        sage: {
          DEFAULT: "#dde3d7",
          deep: "#8b957d",
        },
        slate: {
          50: "#f7f8f6",
          100: "#eef1ed",
          200: "#dfe4de",
          300: "#c9d0c8",
          400: "#929d94",
          500: "#68736a",
          600: "#4d584f",
          700: "#364139",
          800: "#27312b",
          900: "#1d2520",
          950: "#101612",
        },
        brand: {
          50: "#fff4ee",
          100: "#fee8dc",
          200: "#facdb8",
          300: "#f1a17d",
          400: "#dd6a3f",
          500: "#cb5028",
          600: "#bb431d",
          700: "#8e2e12",
          800: "#722814",
          900: "#5d2517",
        },
        danger: {
          50: "#fef2f2",
          200: "#fecaca",
          300: "#fca5a5",
          500: "#ef4444",
          600: "#dc2626",
          700: "#b91c1c",
        },
        success: {
          50: "#f0fdf4",
          200: "#bbf7d0",
          300: "#86efac",
          500: "#22c55e",
          600: "#16a34a",
          700: "#15803d",
        },
        warning: {
          50: "#fff8e5",
          200: "#f1dca7",
          500: "#c9912d",
          600: "#9c6c1c",
          700: "#745018",
        },
      },
      fontFamily: {
        sans: ["Inter", "ui-sans-serif", "-apple-system", "BlinkMacSystemFont", '"Segoe UI"', "sans-serif"],
        // Kept as an alias for older screens. Product headings intentionally
        // use the same sans family as controls and data.
        display: [
          "Inter",
          "ui-sans-serif",
          "-apple-system",
          "BlinkMacSystemFont",
          '"Segoe UI"',
          "sans-serif",
        ],
      },
      boxShadow: {
        sm: "0 1px 2px rgba(16, 22, 18, 0.04)",
        DEFAULT: "0 3px 10px rgba(16, 22, 18, 0.06)",
        md: "0 8px 20px rgba(16, 22, 18, 0.08)",
        lg: "0 14px 32px rgba(16, 22, 18, 0.10)",
        xl: "0 20px 44px rgba(16, 22, 18, 0.12)",
        savvy: "0 12px 28px rgba(16, 22, 18, 0.10)",
        "savvy-sm": "0 1px 3px rgba(16, 22, 18, 0.05)",
      },
      borderRadius: {
        sm: "0.375rem",
        DEFAULT: "0.5rem",
        md: "0.625rem",
        lg: "0.75rem",
        xl: "0.875rem",
        "2xl": "1rem",
        "3xl": "1.25rem",
      },
    },
  },
  plugins: [],
};
