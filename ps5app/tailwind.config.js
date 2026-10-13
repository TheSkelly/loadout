// Loadout's PS5 colors: Carolina blue on royal and midnight blue and black,
// like the app's icon. Sizes are logical pixels of a 1280-wide screen.
export default {theme: {
  fontSize: {xs: ['13px', '18px'], sm: ['15px', '21px'], base: ['17px', '24px'], lg: ['20px', '28px'], xl: ['26px', '32px'], title: ['34px', '40px']},
  extend: {
    colors: {
      ink: '#060A16', ink2: '#0C1638', surface: '#0E1630', raised: '#17234C', line: '#24356A',
      text: '#E8F1FB', muted: '#8FA3C4', faint: '#5A6B8E',
      accent: '#4B9CD3', royal: '#2F5BD8', mint: '#5BC8A0', coral: '#EF6F6C', sky: '#7CC4F5',
    },
    fontFamily: {inter: './assets/Inter-Regular.ttf'},
  },
}};
