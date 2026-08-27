/** @type {import('tailwindcss').Config} */
// Design tokens shared with plybox.sh: warm terminal black, apricot
// action, cool-blue secondary text. Keep in sync with app/globals.css.
module.exports = {
  content: ["./web/templates/*.html"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        ground: "#0e0a07",
        card: "#171310",
        edge: "#3a2a20",
        ink: "#e7e9e5",
        fade: "#9fb9ce",
        accent: "#ffbd81",
        "accent-bright": "#ffd2a6",
        deep: "#5a3828",
        steel: "#1c1918",
        "steel-edge": "#4a3d31",
      },
    },
  },
  plugins: [],
};
