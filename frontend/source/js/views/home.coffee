class App.Views.HomePage extends App.Views.Base
  template: _.template($("#home_page_template").html())
  className: 'home'

  events:
    'click a.markAllRead' : 'markAllRead'

  initialize: ->
    super()
    @header = @createChild App.Views.LoggedInHeader
    @unreadItemsView = @createChild App.Views.UnreadItemsList, collection: []
    @fetch()

  fetch: ->
    conn.getUnreadItems (data)=>
      @unreadItemsView.collection = data
      @unreadItemsView.render()

  render: ->
    @$el.html @header.render().$el
    @$el.append @template()
    @$el.append @unreadItemsView.render().$el
    @

  markAllRead: (e)->
    e.preventDefault()
    $.ajax(
      url: "/api/items/unread/mark_multiple_read",
      method: "POST",
      contentType : "application/json",
      data: JSON.stringify({itemIDs: _.map(@unreadItems, (i)-> i.id)})
    ).success => @fetch()

class App.Views.UnreadItemsList extends App.Views.Base
  tagName: 'ul'
  className: 'unreadItems'

  initialize: ->
    super()
    $(document).on 'keydown', (e)=> @keyDown(e)

  render: ->
    @$el.empty()

    @itemViews = for model in @collection
      @createChild App.Views.UnreadItem, model: model
    if @itemViews.length > 0
      @selected = @itemViews[0]
      @selected.select()

    for itemView in @itemViews
      @$el.append itemView.render().$el
    @

  keyDown: (e)->
    switch e.which
      # j
      when 74 then @selectNext()
      # k
      when 75 then @selectPrevious()
      # v
      when 86 then @viewSelected()

  selectNext: ->
    return if @itemViews.length == 0

    idx = @itemViews.indexOf(@selected) + 1
    return if idx >= @itemViews.length

    @selected.deselect()
    @selected.render()

    @selected = @itemViews[idx]

    @selected.select()
    @selected.render()
    @selected.ensureVisible()

  selectPrevious: ->
    return if @itemViews.length == 0

    idx = @itemViews.indexOf(@selected) - 1
    return if idx < 0

    @selected.deselect()
    @selected.render()

    @selected = @itemViews[idx]

    @selected.select()
    @selected.render()
    @selected.ensureVisible()

  viewSelected: ->
    return unless @selected
    @selected.view()

  remove: ->
    $(document).off 'keydown'
    super()

class App.Views.UnreadItem extends App.Views.Base
  tagName: 'li'
  template: _.template($("#item_template").html())

  events:
    'click a' : 'view'

  render: ->
    @$el.html @template(@model)
    if @isSelected
      @$el.addClass 'selected'
    else
      @$el.removeClass 'selected'
    @

  view: (e)->
    e.preventDefault() if e
    @model.markRead()
    window.open(@model.get('url'))

  select: ->
    @isSelected = true

  deselect: ->
    @model.markRead()
    @isSelected = false

  ensureVisible: ->
    @el.scrollIntoView() unless @isEntirelyVisible()

  isEntirelyVisible: ->
    rect = @el.getBoundingClientRect()
    rect.top >= 0 and rect.left >= 0 and rect.bottom <= window.innerHeight and rect.right <= window.innerWidth
