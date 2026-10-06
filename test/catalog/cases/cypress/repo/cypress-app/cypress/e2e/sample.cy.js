describe('kubetest catalog', () => {
  it('serves the page', () => {
    cy.visit('/');
    cy.contains('ok');
  });

  it('fails on purpose', () => {
    cy.visit('/');
    cy.contains('this text is not on the page', { timeout: 1000 });
  });
});
