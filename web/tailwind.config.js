/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx,js,jsx}"],
  theme: {
    extend: {
      colors: {
        brand: {
          500: "#14b8a6",
          600: "#0d9488"
        }
      }
    }
  },
  plugins: []
};
