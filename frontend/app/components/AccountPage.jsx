import React from 'react';
import {conn} from '../connection.js'
import Session from '../session.js'
import{toTPRString} from '../date.js'

export default class AccountPage extends React.Component {
  constructor(props, context) {
    super(props, context)

    this.state = {
      email: '',
      existingPassword: '',
      newPassword: '',
      passwordConfirmation: ''
    }

    this.handleChange = this.handleChange.bind(this)
    this.fetch = this.fetch.bind(this)
    this.update = this.update.bind(this)
  }

  handleChange(name, event) {
    var h = {}
    h[name] = event.target.value
    this.setState(h)
  }

  componentDidMount() {
    this.fetch()
  }

  fetch() {
    conn.getAccount({
      succeeded: function(data) {
        this.setState(data)
      }.bind(this)
    })
  }

  render() {
    return (
      <div className="account">
        <form onSubmit={this.update}>
          <dl>
            <dt>
              <label htmlFor="email">Email</label>
            </dt>
            <dd>
              <input type="email" name="email" id="email" value={this.state.email} onChange={this.handleChange.bind(null, "email")} />
            </dd>
            <dt>
              <label htmlFor="existingPassword">Existing Password</label>
            </dt>
            <dd>
              <input type="password" name="existingPassword" id="existingPassword" value={this.state.existingPassword} onChange={this.handleChange.bind(null, "existingPassword")} />
            </dd>
            <dt>
              <label htmlFor="newPassword">New Password</label>
            </dt>
            <dd>
              <input type="password" name="newPassword" id="newPassword" value={this.state.newPassword} onChange={this.handleChange.bind(null, "newPassword")} />
            </dd>
            <dt>
              <label htmlFor="passwordConfirmation">Password Confirmation</label>
            </dt>
            <dd>
              <input type="password" name="passwordConfirmation" id="passwordConfirmation" value={this.state.passwordConfirmation} onChange={this.handleChange.bind(null, "passwordConfirmation")} />
            </dd>
          </dl>

          <input type="submit" value="Update" />
        </form>
      </div>
    )
  }

  update(e) {
    e.preventDefault()

    if(this.state.newPassword != this.state.passwordConfirmation) {
      alert("New password and confirmation must match.")
      return
    }

    var update = {}
    update.email = this.state.email
    update.existingPassword = this.state.existingPassword
    update.newPassword = this.state.newPassword

    conn.updateAccount(update, {
      succeeded: function() {
        this.setState({
          existingPassword: "",
          newPassword: "",
          passwordConfirmation: ""
        })
        alert("Update succeeded")
      }.bind(this),
      failed: function(data) {
        alert(data)
      }.bind(this)
    })
  }
}
