// Loadout's PS5 colors: Orbit-style black and greys, with the icon's
// Carolina and royal blue for focus and highlights. Sizes are logical pixels of a 1280-wide screen.
export default {theme: {
  fontSize: {xs: ['13px', '18px'], sm: ['15px', '21px'], base: ['17px', '24px'], lg: ['20px', '28px'], xl: ['26px', '32px'], title: ['34px', '40px']},
  extend: {
    colors: {
      ink: '#05070C', ink2: '#0B1324', surface: '#12161F', raised: '#1E2433', line: '#2B3346',
      text: '#F3F6FA', muted: '#9AA5B8', faint: '#5E687C',
      accent: '#4B9CD3', royal: '#2F5BD8', mint: '#5BC8A0', coral: '#EF6F6C', sky: '#7CC4F5',
    },
    fontFamily: {inter: './assets/Inter-Regular.ttf'},
  },
}};
